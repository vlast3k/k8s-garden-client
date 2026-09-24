package main

import (
	"testing"

	"code.cloudfoundry.org/rep/cmd/rep/config"
)

func TestRepURL(t *testing.T) {
	repConfig := config.RepConfig{
		CellID:              "cell-id",
		ListenAddrSecurable: "0.0.0.0:1801",
	}

	t.Run("uses the node IP by default", func(t *testing.T) {
		t.Setenv("NODE_IP", "10.0.0.1")
		t.Setenv("REP_ADVERTISE_DOMAIN", "")

		if actual := repURL(repConfig); actual != "https://10.0.0.1:1801" {
			t.Fatalf("expected node IP callback URL, got %q", actual)
		}
	})

	t.Run("uses the cell ID under the configured advertise domain", func(t *testing.T) {
		t.Setenv("NODE_IP", "10.0.0.1")
		t.Setenv("REP_ADVERTISE_DOMAIN", "cells.example.internal.")

		if actual := repURL(repConfig); actual != "https://cell-id.cells.example.internal:1801" {
			t.Fatalf("expected DNS callback URL, got %q", actual)
		}
	})
}
