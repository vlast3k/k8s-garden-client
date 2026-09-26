package k8sgarden_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"syscall"
	"time"

	"code.cloudfoundry.org/garden"
	"code.cloudfoundry.org/k8s-garden-client/pkg/k8sgarden"
	"code.cloudfoundry.org/k8s-garden-client/pkg/k8sgarden/containerd/containerdfakes"
	"code.cloudfoundry.org/lager/v3/lagertest"
	ctrdclient "github.com/containerd/containerd/v2/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/runtime-spec/specs-go"
)

var _ = Describe("Process", func() {
	var (
		testProcess  k8sgarden.Process
		logger       *lagertest.TestLogger
		fakeTask     *containerdfakes.FakeTask
		fakeProcess  *containerdfakes.FakeProcess
		processSpec  *specs.Process
		processIO    garden.ProcessIO
		exitChan     chan ctrdclient.ExitStatus
		processCalls []string
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("process-test")
		exitChan = make(chan ctrdclient.ExitStatus, 1)
		processCalls = nil

		fakeProcess = &containerdfakes.FakeProcess{}
		fakeProcess.WaitCalls(func(context.Context) (<-chan ctrdclient.ExitStatus, error) {
			processCalls = append(processCalls, "wait")
			return exitChan, nil
		})
		fakeProcess.StartCalls(func(context.Context) error {
			processCalls = append(processCalls, "start")
			return nil
		})

		fakeTask = &containerdfakes.FakeTask{}
		fakeTask.IDReturns("test-task")
		fakeTask.PidReturns(54321)
		fakeTask.ExecReturns(fakeProcess, nil)

		processSpec = &specs.Process{
			Args: []string{"/bin/bash", "-c", "sleep 1000"},
			Cwd:  "/home/test",
			Env:  []string{"TEST_ENV=value", "HOME=/home/test", "PATH=/usr/bin"},
			User: specs.User{
				UID: 1001,
				GID: 1002,
			},
		}

		processIO = garden.ProcessIO{
			Stdin:  io.NopCloser(strings.NewReader("input data")),
			Stdout: io.Discard,
			Stderr: io.Discard,
		}

		var err error
		testProcess, err = k8sgarden.NewProcess(
			logger,
			"test-process",
			processSpec,
			processIO,
			fakeTask,
		)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("ID", func() {
		It("returns the correct ID", func() {
			Expect(testProcess.ID()).To(Equal("test-process"))
		})
	})

	Describe("Wait and Signal", func() {
		It("starts the process before waiting and sends signals correctly", func() {
			Expect(fakeTask.ExecCallCount()).To(Equal(1))
			Expect(fakeProcess.WaitCallCount()).To(Equal(1))
			Expect(fakeProcess.StartCallCount()).To(Equal(1))
			Expect(processCalls).To(Equal([]string{"wait", "start"}))

			exitStatus := ctrdclient.NewExitStatus(42, time.Now(), nil)
			exitChan <- *exitStatus
			exitCode, err := testProcess.Wait()
			Expect(err).NotTo(HaveOccurred())
			Expect(exitCode).To(Equal(42))

			Expect(testProcess.Signal(garden.SignalTerminate)).To(Succeed())
			Expect(fakeProcess.KillCallCount()).To(Equal(1))
			_, signal, _ := fakeProcess.KillArgsForCall(0)
			Expect(signal).To(Equal(syscall.SIGTERM))

			Expect(testProcess.Signal(garden.SignalKill)).To(Succeed())
			Expect(fakeProcess.KillCallCount()).To(Equal(2))
			_, signal, _ = fakeProcess.KillArgsForCall(1)
			Expect(signal).To(Equal(syscall.SIGKILL))
		})
	})

	Describe("creation failures", func() {
		It("returns an exec error", func() {
			fakeTask.ExecReturns(nil, errors.New("exec failed"))

			_, err := k8sgarden.NewProcess(logger, "failed-process", processSpec, processIO, fakeTask)

			Expect(err).To(MatchError("exec failed"))
		})

		It("cleans up when registering for exit fails", func() {
			fakeProcess.WaitReturns(nil, errors.New("wait failed"))

			_, err := k8sgarden.NewProcess(logger, "failed-process", processSpec, processIO, fakeTask)

			Expect(err).To(MatchError("wait failed"))
			Expect(fakeProcess.StartCallCount()).To(Equal(1))
			Expect(fakeProcess.DeleteCallCount()).To(Equal(1))
		})

		It("cleans up when starting fails", func() {
			fakeProcess.StartReturns(errors.New("start failed"))

			_, err := k8sgarden.NewProcess(logger, "failed-process", processSpec, processIO, fakeTask)

			Expect(err).To(MatchError("start failed"))
			Expect(fakeProcess.DeleteCallCount()).To(Equal(1))
		})
	})
})
