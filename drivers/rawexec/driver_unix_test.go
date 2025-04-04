// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: BUSL-1.1

//go:build !windows

package rawexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/nomad/ci"
	clienttestutil "github.com/hashicorp/nomad/client/testutil"
	"github.com/hashicorp/nomad/helper/testtask"
	"github.com/hashicorp/nomad/helper/users"
	"github.com/hashicorp/nomad/helper/uuid"
	"github.com/hashicorp/nomad/plugins/base"
	basePlug "github.com/hashicorp/nomad/plugins/base"
	"github.com/hashicorp/nomad/plugins/drivers"
	dtestutil "github.com/hashicorp/nomad/plugins/drivers/testutils"
	"github.com/hashicorp/nomad/testutil"
	"github.com/shoenig/test/must"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRawExecDriver_User(t *testing.T) {
	ci.Parallel(t)
	clienttestutil.RequireLinux(t)
	require := require.New(t)

	d := newEnabledRawExecDriver(t)
	harness := dtestutil.NewDriverHarness(t, d)

	task := &drivers.TaskConfig{
		ID:   uuid.Generate(),
		Name: "sleep",
		User: "alice",
	}

	cleanup := harness.MkAllocDir(task, false)
	defer cleanup()

	tc := &TaskConfig{
		Command: testtask.Path(),
		Args:    []string{"sleep", "45s"},
	}
	require.NoError(task.EncodeConcreteDriverConfig(&tc))
	testtask.SetTaskConfigEnv(task)

	_, _, err := harness.StartTask(task)
	require.Error(err)
	msg := "unknown user alice"
	require.Contains(err.Error(), msg)
}

func TestRawExecDriver_Signal(t *testing.T) {
	ci.Parallel(t)
	clienttestutil.RequireLinux(t)

	require := require.New(t)

	d := newEnabledRawExecDriver(t)
	harness := dtestutil.NewDriverHarness(t, d)

	allocID := uuid.Generate()
	taskName := "signal"
	task := &drivers.TaskConfig{
		AllocID:   allocID,
		ID:        uuid.Generate(),
		Name:      taskName,
		Env:       defaultEnv(),
		Resources: testResources(allocID, taskName),
	}

	cleanup := harness.MkAllocDir(task, true)
	defer cleanup()

	harness.MakeTaskCgroup(allocID, taskName)

	tc := &TaskConfig{
		Command: "/bin/bash",
		Args:    []string{"test.sh"},
	}
	require.NoError(task.EncodeConcreteDriverConfig(&tc))
	testtask.SetTaskConfigEnv(task)

	testFile := filepath.Join(task.TaskDir().Dir, "test.sh")
	testData := []byte(`
at_term() {
    echo 'Terminated.'
    exit 3
}
trap at_term USR1
while true; do
    sleep 1
done
	`)
	require.NoError(os.WriteFile(testFile, testData, 0777))

	_, _, err := harness.StartTask(task)
	require.NoError(err)

	go func() {
		time.Sleep(100 * time.Millisecond)
		require.NoError(harness.SignalTask(task.ID, "SIGUSR1"))
	}()

	// Task should terminate quickly
	waitCh, err := harness.WaitTask(context.Background(), task.ID)
	require.NoError(err)
	select {
	case res := <-waitCh:
		require.False(res.Successful())
		require.Equal(3, res.ExitCode)
	case <-time.After(time.Duration(testutil.TestMultiplier()*6) * time.Second):
		require.Fail("WaitTask timeout")
	}

	// Check the log file to see it exited because of the signal
	outputFile := filepath.Join(task.TaskDir().LogDir, "signal.stdout.0")
	exp := "Terminated."
	testutil.WaitForResult(func() (bool, error) {
		act, err := os.ReadFile(outputFile)
		if err != nil {
			return false, fmt.Errorf("Couldn't read expected output: %v", err)
		}

		if strings.TrimSpace(string(act)) != exp {
			t.Logf("Read from %v", outputFile)
			return false, fmt.Errorf("Command outputted %v; want %v", act, exp)
		}
		return true, nil
	}, func(err error) { require.NoError(err) })
}

func TestRawExecDriver_StartWaitStop(t *testing.T) {
	ci.Parallel(t)
	require := require.New(t)

	d := newEnabledRawExecDriver(t)
	harness := dtestutil.NewDriverHarness(t, d)
	defer harness.Kill()

	config := &Config{Enabled: true}
	var data []byte
	require.NoError(base.MsgPackEncode(&data, config))
	bconfig := &base.Config{
		PluginConfig: data,
		AgentConfig: &base.AgentConfig{
			Driver: &base.ClientDriverConfig{
				Topology: d.nomadConfig.Topology,
			},
		},
	}
	require.NoError(harness.SetConfig(bconfig))

	allocID := uuid.Generate()
	taskName := "test"
	task := &drivers.TaskConfig{
		AllocID:   allocID,
		ID:        uuid.Generate(),
		Name:      taskName,
		Resources: testResources(allocID, taskName),
	}

	taskConfig := map[string]interface{}{}
	taskConfig["command"] = testtask.Path()
	taskConfig["args"] = []string{"sleep", "100s"}

	require.NoError(task.EncodeConcreteDriverConfig(&taskConfig))

	cleanup := harness.MkAllocDir(task, false)
	defer cleanup()

	harness.MakeTaskCgroup(allocID, taskName)

	handle, _, err := harness.StartTask(task)
	require.NoError(err)

	ch, err := harness.WaitTask(context.Background(), handle.Config.ID)
	require.NoError(err)

	require.NoError(harness.WaitUntilStarted(task.ID, 1*time.Second))

	go func() {
		harness.StopTask(task.ID, 2*time.Second, "SIGINT")
	}()

	select {
	case result := <-ch:
		require.Equal(int(unix.SIGINT), result.Signal)
	case <-time.After(10 * time.Second):
		require.Fail("timeout waiting for task to shutdown")
	}

	// Ensure that the task is marked as dead, but account
	// for WaitTask() closing channel before internal state is updated
	testutil.WaitForResult(func() (bool, error) {
		status, err := harness.InspectTask(task.ID)
		if err != nil {
			return false, fmt.Errorf("inspecting task failed: %v", err)
		}
		if status.State != drivers.TaskStateExited {
			return false, fmt.Errorf("task hasn't exited yet; status: %v", status.State)
		}

		return true, nil
	}, func(err error) {
		require.NoError(err)
	})

	require.NoError(harness.DestroyTask(task.ID, true))
}

// TestRawExecDriver_DestroyKillsAll asserts that when TaskDestroy is called all
// task processes are cleaned up.
func TestRawExecDriver_DestroyKillsAll(t *testing.T) {
	ci.Parallel(t)
	clienttestutil.RequireLinux(t)

	d := newEnabledRawExecDriver(t)
	harness := dtestutil.NewDriverHarness(t, d)
	defer harness.Kill()

	allocID := uuid.Generate()
	taskName := "test"
	task := &drivers.TaskConfig{
		AllocID:   allocID,
		ID:        uuid.Generate(),
		Name:      taskName,
		Env:       defaultEnv(),
		Resources: testResources(allocID, taskName),
	}

	cleanup := harness.MkAllocDir(task, true)
	defer cleanup()

	harness.MakeTaskCgroup(allocID, taskName)

	taskConfig := map[string]interface{}{}
	taskConfig["command"] = "/bin/sh"
	taskConfig["args"] = []string{"-c", fmt.Sprintf(`sleep 3600 & echo "SLEEP_PID=$!"`)}

	require.NoError(t, task.EncodeConcreteDriverConfig(&taskConfig))

	handle, _, err := harness.StartTask(task)
	require.NoError(t, err)
	defer harness.DestroyTask(task.ID, true)

	ch, err := harness.WaitTask(context.Background(), handle.Config.ID)
	require.NoError(t, err)

	select {
	case result := <-ch:
		require.True(t, result.Successful(), "command failed: %#v", result)
	case <-time.After(10 * time.Second):
		require.Fail(t, "timeout waiting for task to shutdown")
	}

	sleepPid := 0

	// Ensure that the task is marked as dead, but account
	// for WaitTask() closing channel before internal state is updated
	testutil.WaitForResult(func() (bool, error) {
		stdout, err := os.ReadFile(filepath.Join(task.TaskDir().LogDir, "test.stdout.0"))
		if err != nil {
			return false, fmt.Errorf("failed to output pid file: %v", err)
		}

		pidMatch := regexp.MustCompile(`SLEEP_PID=(\d+)`).FindStringSubmatch(string(stdout))
		if len(pidMatch) != 2 {
			return false, fmt.Errorf("failed to find pid in %s", string(stdout))
		}

		pid, err := strconv.Atoi(pidMatch[1])
		if err != nil {
			return false, fmt.Errorf("pid parts aren't int: %s", pidMatch[1])
		}

		sleepPid = pid
		return true, nil
	}, func(err error) {
		require.NoError(t, err)
	})

	// isProcessRunning returns an error if process is not running
	isProcessRunning := func(pid int) error {
		process, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("failed to find process: %s", err)
		}

		err = process.Signal(syscall.Signal(0))
		if err != nil {
			return fmt.Errorf("failed to signal process: %s", err)
		}

		return nil
	}

	require.NoError(t, isProcessRunning(sleepPid))

	require.NoError(t, harness.DestroyTask(task.ID, true))

	testutil.WaitForResult(func() (bool, error) {
		err := isProcessRunning(sleepPid)
		if err == nil {
			return false, fmt.Errorf("child process is still running")
		}

		if !strings.Contains(err.Error(), "failed to signal process") {
			return false, fmt.Errorf("unexpected error: %v", err)
		}

		return true, nil
	}, func(err error) {
		require.NoError(t, err)
	})
}

func TestRawExec_ExecTaskStreaming(t *testing.T) {
	ci.Parallel(t)
	if runtime.GOOS == "darwin" {
		t.Skip("skip running exec tasks on darwin as darwin has restrictions on starting tty shells")
	}
	require := require.New(t)

	d := newEnabledRawExecDriver(t)
	harness := dtestutil.NewDriverHarness(t, d)
	defer harness.Kill()

	allocID := uuid.Generate()
	taskName := "sleep"
	task := &drivers.TaskConfig{
		AllocID:   allocID,
		ID:        uuid.Generate(),
		Name:      taskName,
		Env:       defaultEnv(),
		Resources: testResources(allocID, taskName),
	}

	cleanup := harness.MkAllocDir(task, false)
	defer cleanup()

	harness.MakeTaskCgroup(allocID, taskName)

	tc := &TaskConfig{
		Command: testtask.Path(),
		Args:    []string{"sleep", "9000s"},
	}
	require.NoError(task.EncodeConcreteDriverConfig(&tc))
	testtask.SetTaskConfigEnv(task)

	_, _, err := harness.StartTask(task)
	require.NoError(err)
	defer d.DestroyTask(task.ID, true)

	dtestutil.ExecTaskStreamingConformanceTests(t, harness, task.ID)

}

func TestRawExec_ExecTaskStreaming_User(t *testing.T) {
	t.Skip("todo(shoenig): this test has always been broken, now we skip instead of paving over it")
	ci.Parallel(t)
	clienttestutil.RequireLinux(t)

	d := newEnabledRawExecDriver(t)

	harness := dtestutil.NewDriverHarness(t, d)
	defer harness.Kill()

	allocID := uuid.Generate()
	taskName := "sleep"
	task := &drivers.TaskConfig{
		AllocID:   allocID,
		ID:        uuid.Generate(),
		Name:      taskName,
		User:      "nobody",
		Resources: testResources(allocID, taskName),
	}

	cleanup := harness.MkAllocDir(task, false)
	defer cleanup()

	harness.MakeTaskCgroup(allocID, taskName)

	err := os.Chmod(task.AllocDir, 0777)
	require.NoError(t, err)

	tc := &TaskConfig{
		Command: "/bin/sleep",
		Args:    []string{"9000"},
	}
	require.NoError(t, task.EncodeConcreteDriverConfig(&tc))
	testtask.SetTaskConfigEnv(task)

	_, _, err = harness.StartTask(task)
	require.NoError(t, err)
	defer d.DestroyTask(task.ID, true)

	code, stdout, stderr := dtestutil.ExecTask(t, harness, task.ID, "whoami", false, "")
	require.Zero(t, code)
	require.Empty(t, stderr)
	require.Contains(t, stdout, "nobody")
}

func TestRawExecDriver_StartWaitRecoverWaitStop(t *testing.T) {
	ci.Parallel(t)
	require := require.New(t)

	d := newEnabledRawExecDriver(t)
	harness := dtestutil.NewDriverHarness(t, d)
	defer harness.Kill()

	config := &Config{Enabled: true}
	var data []byte

	require.NoError(basePlug.MsgPackEncode(&data, config))
	bconfig := &basePlug.Config{
		PluginConfig: data,
		AgentConfig: &base.AgentConfig{
			Driver: &base.ClientDriverConfig{
				Topology: d.nomadConfig.Topology,
			},
		},
	}
	require.NoError(harness.SetConfig(bconfig))

	allocID := uuid.Generate()
	taskName := "sleep"
	task := &drivers.TaskConfig{
		AllocID:   allocID,
		ID:        uuid.Generate(),
		Name:      taskName,
		Env:       defaultEnv(),
		Resources: testResources(allocID, taskName),
	}

	tc := &TaskConfig{
		Command: testtask.Path(),
		Args:    []string{"sleep", "100s"},
	}
	require.NoError(task.EncodeConcreteDriverConfig(&tc))

	testtask.SetTaskConfigEnv(task)

	cleanup := harness.MkAllocDir(task, false)
	defer cleanup()

	harness.MakeTaskCgroup(allocID, taskName)

	handle, _, err := harness.StartTask(task)
	require.NoError(err)

	ch, err := harness.WaitTask(context.Background(), task.ID)
	require.NoError(err)

	var waitDone bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		result := <-ch
		require.Error(result.Err)
		waitDone = true
	}()

	originalStatus, err := d.InspectTask(task.ID)
	require.NoError(err)

	d.tasks.Delete(task.ID)

	wg.Wait()
	require.True(waitDone)
	_, err = d.InspectTask(task.ID)
	require.Equal(drivers.ErrTaskNotFound, err)

	err = d.RecoverTask(handle)
	require.NoError(err)

	status, err := d.InspectTask(task.ID)
	require.NoError(err)
	require.Exactly(originalStatus, status)

	ch, err = harness.WaitTask(context.Background(), task.ID)
	require.NoError(err)

	wg.Add(1)
	waitDone = false
	go func() {
		defer wg.Done()
		result := <-ch
		require.NoError(result.Err)
		require.NotZero(result.ExitCode)
		require.Equal(9, result.Signal)
		waitDone = true
	}()

	time.Sleep(300 * time.Millisecond)
	require.NoError(d.StopTask(task.ID, 0, "SIGKILL"))
	wg.Wait()
	require.NoError(d.DestroyTask(task.ID, false))
	require.True(waitDone)
}

func TestRawExec_Validate(t *testing.T) {
	ci.Parallel(t)

	current, err := users.Current()
	must.NoError(t, err)

	currentUserErrStr := fmt.Sprintf("running as uid %s is disallowed", current.Uid)

	allowAll := ""
	denyCurrent := current.Uid

	configAllowCurrent := Config{DeniedHostUids: allowAll}
	configDenyCurrent := Config{DeniedHostUids: denyCurrent}

	driverConfigNoUserSpecified := drivers.TaskConfig{}
	driverTaskConfig := drivers.TaskConfig{User: current.Name}

	for _, tc := range []struct {
		config       Config
		driverConfig drivers.TaskConfig
		exp          error
	}{
		{
			config:       configAllowCurrent,
			driverConfig: driverTaskConfig,
			exp:          nil,
		},
		{
			config:       configDenyCurrent,
			driverConfig: driverConfigNoUserSpecified,
			exp:          errors.New(currentUserErrStr),
		},
		{
			config:       configDenyCurrent,
			driverConfig: driverTaskConfig,
			exp:          errors.New(currentUserErrStr),
		},
	} {

		d := newEnabledRawExecDriver(t)

		// Force the creation of the validatior, the mock is used by newEnabledRawExecDriver by default
		d.userIDValidator = nil

		harness := dtestutil.NewDriverHarness(t, d)
		defer harness.Kill()

		config := tc.config

		var data []byte

		must.NoError(t, base.MsgPackEncode(&data, config))
		bconfig := &base.Config{
			PluginConfig: data,
			AgentConfig: &base.AgentConfig{
				Driver: &base.ClientDriverConfig{
					Topology: d.nomadConfig.Topology,
				},
			},
		}

		must.NoError(t, harness.SetConfig(bconfig))
		must.Eq(t, tc.exp, d.Validate(tc.driverConfig))
	}
}

func TestRawExecDriver_ExecutorKilled_ExitCode(t *testing.T) {
	ci.Parallel(t)
	clienttestutil.ExecCompatible(t)

	d := newEnabledRawExecDriver(t)
	harness := dtestutil.NewDriverHarness(t, d)
	defer harness.Kill()

	allocID := uuid.Generate()
	taskName := "sleep"
	task := &drivers.TaskConfig{
		AllocID:   allocID,
		ID:        uuid.Generate(),
		Name:      taskName,
		Env:       defaultEnv(),
		Resources: testResources(allocID, taskName),
	}

	cleanup := harness.MkAllocDir(task, false)
	defer cleanup()

	tc := &TaskConfig{
		Command: testtask.Path(),
		Args:    []string{"sleep", "10s"},
	}
	must.NoError(t, task.EncodeConcreteDriverConfig(&tc))
	testtask.SetTaskConfigEnv(task)

	harness.MakeTaskCgroup(allocID, taskName)
	handle, _, err := harness.StartTask(task)
	must.NoError(t, err)

	// Decode driver state to get executor PID
	var driverState TaskState
	must.NoError(t, handle.GetDriverState(&driverState))

	// Kill the executor and wait until it's gone
	pid := driverState.ReattachConfig.Pid
	must.NoError(t, err)
	must.NoError(t, syscall.Kill(pid, syscall.SIGKILL))

	// Make sure the right exit code is set
	waitCh, err := harness.WaitTask(context.Background(), task.ID)
	must.NoError(t, err)
	select {
	case res := <-waitCh:
		must.False(t, res.Successful())
		must.Eq(t, -1, res.ExitCode)
		must.Eq(t, false, res.OOMKilled)
	case <-time.After(10 * time.Second):
		must.Unreachable(t, must.Sprint("exceeded wait timeout"))
	}

	must.NoError(t, harness.DestroyTask(task.ID, true))
}
