// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: BUSL-1.1

//go:build ent
// +build ent

package command

import (
	"strings"
	"testing"

	"github.com/hashicorp/cli"
	"github.com/hashicorp/nomad/api"
	"github.com/hashicorp/nomad/ci"
	"github.com/hashicorp/nomad/helper/pointer"
	"github.com/hashicorp/nomad/helper/uuid"
	"github.com/posener/complete"
	"github.com/shoenig/test/must"
)

func TestQuotaDeleteCommand_Implements(t *testing.T) {
	ci.Parallel(t)
	var _ cli.Command = &QuotaDeleteCommand{}
}

func TestQuotaDeleteCommand_Fails(t *testing.T) {
	ci.Parallel(t)
	ui := cli.NewMockUi()
	cmd := &QuotaDeleteCommand{Meta: Meta{Ui: ui}}

	// Fails on misuse
	if code := cmd.Run([]string{"some", "bad", "args"}); code != 1 {
		t.Fatalf("expected exit code 1, got: %d", code)
	}
	if out := ui.ErrorWriter.String(); !strings.Contains(out, commandErrorText(cmd)) {
		t.Fatalf("expected help output, got: %s", out)
	}
	ui.ErrorWriter.Reset()

	if code := cmd.Run([]string{"-address=nope", "foo"}); code != 1 {
		t.Fatalf("expected exit code 1, got: %d", code)
	}
	if out := ui.ErrorWriter.String(); !strings.Contains(out, "deleting quota") {
		t.Fatalf("connection error, got: %s", out)
	}
	ui.ErrorWriter.Reset()
}

func TestQuotaDeleteCommand_Good(t *testing.T) {
	ci.Parallel(t)

	// Create a server
	srv, client, url := testServer(t, true, nil)
	defer srv.Shutdown()

	ui := cli.NewMockUi()
	cmd := &QuotaDeleteCommand{Meta: Meta{Ui: ui}}

	// Create a quota to delete
	qs := testQuotaSpec()
	_, err := client.Quotas().Register(qs, nil)
	must.NoError(t, err)

	// Delete a namespace
	if code := cmd.Run([]string{"-address=" + url, qs.Name}); code != 0 {
		t.Fatalf("expected exit 0, got: %d; %v", code, ui.ErrorWriter.String())
	}

	quotas, _, err := client.Quotas().List(nil)
	must.NoError(t, err)
	must.SliceEmpty(t, quotas)
}

func TestQuotaDeleteCommand_AutocompleteArgs(t *testing.T) {
	ci.Parallel(t)

	srv, client, url := testServer(t, true, nil)
	defer srv.Shutdown()

	ui := cli.NewMockUi()
	cmd := &QuotaDeleteCommand{Meta: Meta{Ui: ui, flagAddress: url}}

	// Create a quota
	qs := testQuotaSpec()
	_, err := client.Quotas().Register(qs, nil)
	must.NoError(t, err)

	args := complete.Args{Last: "quot"}
	predictor := cmd.AutocompleteArgs()

	res := predictor.Predict(args)
	must.Len(t, 1, res)
	must.Eq(t, qs.Name, res[0])
}

// testQuotaSpec returns a test quota specification
func testQuotaSpec() *api.QuotaSpec {
	return &api.QuotaSpec{
		Name: "quota-test-" + uuid.Short(),
		Limits: []*api.QuotaLimit{
			{
				Region: "global",
				RegionLimit: &api.QuotaResources{
					CPU: pointer.Of(100),
				},
			},
		},
	}
}
