// +build functional

package cri_containerd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	runtime "k8s.io/cri-api/pkg/apis/runtime/v1alpha2"
)

// returns a request config for creating a template sandbox
func getTemplatePodConfig(name string) *runtime.RunPodSandboxRequest {
	return &runtime.RunPodSandboxRequest{
		Config: &runtime.PodSandboxConfig{
			Metadata: &runtime.PodSandboxMetadata{
				Name:      name,
				Uid:       "0",
				Namespace: testNamespace,
			},
			Annotations: map[string]string{
				"io.microsoft.virtualmachine.saveastemplate": "true",
			},
		},
		RuntimeHandler: wcowHypervisorRuntimeHandler,
	}
}

// returns a request config for creating a template container
func getTemplateContainerConfig(name string) *runtime.CreateContainerRequest {
	return &runtime.CreateContainerRequest{
		Config: &runtime.ContainerConfig{
			Metadata: &runtime.ContainerMetadata{
				Name: name,
			},
			Image: &runtime.ImageSpec{
				Image: imageWindowsNanoserver,
			},
			// Do not keep the ping running on template containers.
			Command: []string{
				"cmd",
				"/c",
				"ping",
				"127.0.0.1",
			},
			Annotations: map[string]string{
				"io.microsoft.virtualmachine.saveastemplate": "true",
			},
		},
	}
}

// returns a request config for creating a standard container
func getStandardContainerConfig(name string) *runtime.CreateContainerRequest {
	return &runtime.CreateContainerRequest{
		Config: &runtime.ContainerConfig{
			Metadata: &runtime.ContainerMetadata{
				Name: name,
			},
			Image: &runtime.ImageSpec{
				Image: imageWindowsNanoserver,
			},
			Command: []string{
				"cmd",
				"/c",
				"ping",
				"-t",
				"127.0.0.1",
			},
		},
	}
}

// returns a create cloned sandbox request config.
func getClonedPodConfig(uniqueID int, templateid string) *runtime.RunPodSandboxRequest {
	return &runtime.RunPodSandboxRequest{
		Config: &runtime.PodSandboxConfig{
			Metadata: &runtime.PodSandboxMetadata{
				Name:      fmt.Sprintf("clonedpod-%d", uniqueID),
				Uid:       "0",
				Namespace: testNamespace,
			},
			Annotations: map[string]string{
				"io.microsoft.virtualmachine.templateid": templateid + "@vm",
			},
		},
		RuntimeHandler: wcowHypervisorRuntimeHandler,
	}
}

// returns a create cloned container request config.
func getClonedContainerConfig(uniqueID int, templateid string) *runtime.CreateContainerRequest {
	return &runtime.CreateContainerRequest{
		Config: &runtime.ContainerConfig{
			Metadata: &runtime.ContainerMetadata{
				Name: fmt.Sprintf("clonedcontainer-%d", uniqueID),
			},
			Image: &runtime.ImageSpec{
				Image: imageWindowsNanoserver,
			},
			// Command for cloned containers
			Command: []string{
				"cmd",
				"/c",
				"ping",
				"-t",
				"127.0.0.1",
			},
			Annotations: map[string]string{
				"io.microsoft.virtualmachine.templateid": templateid,
			},
		},
	}
}

func waitForTemplateSave(ctx context.Context, t *testing.T, templatePodID string) {
	app := "hcsdiag"
	arg0 := "list"
	for {
		cmd := exec.Command(app, arg0)
		stdout, err := cmd.Output()
		if err != nil {
			t.Fatalf("failed while waiting for save template to finish: %s", err)
		}
		if strings.Contains(string(stdout), templatePodID) && strings.Contains(string(stdout), "SavedAsTemplate") {
			break
		}
		timer := time.NewTimer(time.Millisecond * 100)
		select {
		case <-ctx.Done():
			t.Fatalf("Timelimit exceeded for wait for template saving to finish")
		case <-timer.C:
		}
	}
}

func createPodAndContainer(ctx context.Context, t *testing.T, client runtime.RuntimeServiceClient, sandboxRequest *runtime.RunPodSandboxRequest, containerRequest *runtime.CreateContainerRequest) (podID, containerID string) {
	podID = runPodSandbox(t, client, ctx, sandboxRequest)
	containerRequest.PodSandboxId = podID
	containerRequest.SandboxConfig = sandboxRequest.Config
	containerID = createContainer(t, client, ctx, containerRequest)
	startContainer(t, client, ctx, containerID)
	return podID, containerID
}

// Creates a template sandbox and then a template container inside it.
// Since, template container can take time to finish the init process and then exit (at which
// point it will actually be saved as a template) this function wait until the template is
// actually saved.
// It is the callers responsibility to clean the stop and remove the cloned
// containers and pods.
func createTemplateContainer(ctx context.Context, t *testing.T, client runtime.RuntimeServiceClient, templateSandboxRequest *runtime.RunPodSandboxRequest, templateContainerRequest *runtime.CreateContainerRequest) (templatePodID, templateContainerID string) {
	templatePodID, templateContainerID = createPodAndContainer(ctx, t, client, templateSandboxRequest, templateContainerRequest)

	// Send a context with deadline for waitForTemplateSave function
	d := time.Now().Add(10 * time.Second)
	ctx, cancel := context.WithDeadline(ctx, d)
	defer cancel()
	waitForTemplateSave(ctx, t, templatePodID)
	return
}

// Creates a clone from the given template pod and container.
// It is the callers responsibility to clean the stop and remove the cloned
// containers and pods.
func createClonedContainer(ctx context.Context, t *testing.T, client runtime.RuntimeServiceClient, templatePodID, templateContainerID string, cloneNumber int) (clonedPodID, clonedContainerID string) {
	cloneSandboxRequest := getClonedPodConfig(cloneNumber, templatePodID)
	cloneContainerRequest := getClonedContainerConfig(cloneNumber, templateContainerID)
	clonedPodID, clonedContainerID = createPodAndContainer(ctx, t, client, cloneSandboxRequest, cloneContainerRequest)
	return
}

// Runs a command inside given container and verifies if the command executes successfully.
func verifyContainerExec(ctx context.Context, t *testing.T, client runtime.RuntimeServiceClient, containerID string) {
	execCommand := []string{
		"ping",
		"www.microsoft.com",
	}

	execRequest := &runtime.ExecSyncRequest{
		ContainerId: containerID,
		Cmd:         execCommand,
		Timeout:     20,
	}

	r := execSync(t, client, ctx, execRequest)
	output := strings.TrimSpace(string(r.Stdout))
	errorMsg := string(r.Stderr)
	exitCode := int(r.ExitCode)

	if exitCode != 0 || len(errorMsg) != 0 {
		t.Fatalf("Failed execution inside container %s with error: %s, exitCode: %d", containerID, errorMsg, exitCode)
	} else {
		t.Logf("Exec(container: %s) stdout: %s, stderr: %s, exitCode: %d\n", containerID, output, errorMsg, exitCode)
	}
}

// A simple test to just create a template container and then create one
// cloned container from that template.
func Test_CloneContainer_WCOW(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestRuntimeClient(t)

	pullRequiredImages(t, []string{imageWindowsNanoserver})

	templatePodID, templateContainerID := createTemplateContainer(ctx, t, client, getTemplatePodConfig("templatepod"), getTemplateContainerConfig("templatecontainer"))
	defer removePodSandbox(t, client, ctx, templatePodID)
	defer stopPodSandbox(t, client, ctx, templatePodID)
	defer removeContainer(t, client, ctx, templateContainerID)
	defer stopContainer(t, client, ctx, templateContainerID)

	clonedPodID, clonedContainerID := createClonedContainer(ctx, t, client, templatePodID, templateContainerID, 1)
	defer removePodSandbox(t, client, ctx, clonedPodID)
	defer stopPodSandbox(t, client, ctx, clonedPodID)
	defer removeContainer(t, client, ctx, clonedContainerID)
	defer stopContainer(t, client, ctx, clonedContainerID)

	verifyContainerExec(ctx, t, client, clonedContainerID)
}

// A test for creating multiple clones(5 clones) from one template container.
func Test_MultiplClonedContainers_WCOW(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestRuntimeClient(t)
	nClones := 3

	pullRequiredImages(t, []string{imageWindowsNanoserver})

	// create template pod & container
	templatePodID, templateContainerID := createTemplateContainer(ctx, t, client, getTemplatePodConfig("templatepod"), getTemplateContainerConfig("templatecontainer"))
	defer removePodSandbox(t, client, ctx, templatePodID)
	defer stopPodSandbox(t, client, ctx, templatePodID)
	defer removeContainer(t, client, ctx, templateContainerID)
	defer stopContainer(t, client, ctx, templateContainerID)

	// create multiple clones
	clonedContainers := []string{}
	for i := 0; i < nClones; i++ {
		clonedPodID, clonedContainerID := createClonedContainer(ctx, t, client, templatePodID, templateContainerID, i)
		// cleanup
		defer removePodSandbox(t, client, ctx, clonedPodID)
		defer stopPodSandbox(t, client, ctx, clonedPodID)
		defer removeContainer(t, client, ctx, clonedContainerID)
		defer stopContainer(t, client, ctx, clonedContainerID)
		clonedContainers = append(clonedContainers, clonedContainerID)
	}

	for i := 0; i < nClones; i++ {
		verifyContainerExec(ctx, t, client, clonedContainers[i])
	}
}

// Test if a normal container can be created inside a clond pod alongside the cloned
// container.
// TODO(ambarve): This doesn't work as of now. Enable this test when the bug is fixed.
func DisabledTest_NormalContainerInClonedPod_WCOW(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestRuntimeClient(t)

	// create template pod & container
	templatePodID, templateContainerID := createTemplateContainer(ctx, t, client, getTemplatePodConfig("templatepod"), getTemplateContainerConfig("templatecontainer"))
	defer removePodSandbox(t, client, ctx, templatePodID)
	defer stopPodSandbox(t, client, ctx, templatePodID)
	defer removeContainer(t, client, ctx, templateContainerID)
	defer stopContainer(t, client, ctx, templateContainerID)

	// create a cloned pod and a cloned container
	cloneSandboxRequest := getClonedPodConfig(1, templatePodID)
	cloneContainerRequest := getClonedContainerConfig(1, templateContainerID)
	clonedPodID, clonedContainerID := createPodAndContainer(ctx, t, client, cloneSandboxRequest, cloneContainerRequest)
	defer removePodSandbox(t, client, ctx, clonedPodID)
	defer stopPodSandbox(t, client, ctx, clonedPodID)
	defer removeContainer(t, client, ctx, clonedContainerID)
	defer stopContainer(t, client, ctx, clonedContainerID)

	// create a normal container in cloned pod
	stdContainerRequest := getStandardContainerConfig("standard-container")
	stdContainerRequest.PodSandboxId = clonedPodID
	stdContainerRequest.SandboxConfig = cloneSandboxRequest.Config
	stdContainerID := createContainer(t, client, ctx, stdContainerRequest)
	startContainer(t, client, ctx, stdContainerID)
	defer removeContainer(t, client, ctx, stdContainerID)
	defer stopContainer(t, client, ctx, stdContainerID)

	verifyContainerExec(ctx, t, client, clonedContainerID)
	verifyContainerExec(ctx, t, client, stdContainerID)
}

// A test for cloning multiple pods first and then cloning one container in each
// of those pods.
func Test_CloneContainersWithClonedPodPool_WCOW(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestRuntimeClient(t)
	nClones := 3

	pullRequiredImages(t, []string{imageWindowsNanoserver})

	// create template pod & container
	templatePodID, templateContainerID := createTemplateContainer(ctx, t, client, getTemplatePodConfig("templatepod"), getTemplateContainerConfig("templatecontainer"))
	defer removePodSandbox(t, client, ctx, templatePodID)
	defer stopPodSandbox(t, client, ctx, templatePodID)
	defer removeContainer(t, client, ctx, templateContainerID)
	defer stopContainer(t, client, ctx, templateContainerID)

	// create multiple pods
	clonedPodIDs := []string{}
	clonedSandboxRequests := []*runtime.RunPodSandboxRequest{}
	for i := 0; i < nClones; i++ {
		cloneSandboxRequest := getClonedPodConfig(i, templatePodID)
		clonedPodID := runPodSandbox(t, client, ctx, cloneSandboxRequest)
		clonedPodIDs = append(clonedPodIDs, clonedPodID)
		clonedSandboxRequests = append(clonedSandboxRequests, cloneSandboxRequest)
		defer removePodSandbox(t, client, ctx, clonedPodID)
		defer stopPodSandbox(t, client, ctx, clonedPodID)
	}

	// create multiple clones
	clonedContainers := []string{}
	for i := 0; i < nClones; i++ {
		cloneContainerRequest := getClonedContainerConfig(i, templateContainerID)

		cloneContainerRequest.PodSandboxId = clonedPodIDs[i]
		cloneContainerRequest.SandboxConfig = clonedSandboxRequests[i].Config
		clonedContainerID := createContainer(t, client, ctx, cloneContainerRequest)
		startContainer(t, client, ctx, clonedContainerID)

		// cleanup
		defer removeContainer(t, client, ctx, clonedContainerID)
		defer stopContainer(t, client, ctx, clonedContainerID)

		clonedContainers = append(clonedContainers, clonedContainerID)
	}

	for i := 0; i < nClones; i++ {
		verifyContainerExec(ctx, t, client, clonedContainers[i])
	}
}

func Test_ClonedContainerRunningAfterDeletingTemplate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestRuntimeClient(t)

	pullRequiredImages(t, []string{imageWindowsNanoserver})

	templatePodID, templateContainerID := createTemplateContainer(ctx, t, client, getTemplatePodConfig("templatepod"), getTemplateContainerConfig("templatecontainer"))

	clonedPodID, clonedContainerID := createClonedContainer(ctx, t, client, templatePodID, templateContainerID, 1)
	defer removePodSandbox(t, client, ctx, clonedPodID)
	defer stopPodSandbox(t, client, ctx, clonedPodID)
	defer removeContainer(t, client, ctx, clonedContainerID)
	defer stopContainer(t, client, ctx, clonedContainerID)

	stopPodSandbox(t, client, ctx, templatePodID)
	removePodSandbox(t, client, ctx, templatePodID)

	verifyContainerExec(ctx, t, client, clonedContainerID)

}

// A test to verify that multiple templats can be created and clones
// can be made from each of them simultaneously.
func Test_MultipleTemplateAndClones_WCOW(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestRuntimeClient(t)
	nTemplates := 3

	pullRequiredImages(t, []string{imageWindowsNanoserver})

	templatePodIDs := []string{}
	templateContainerIDs := []string{}
	for i := 0; i < nTemplates; i++ {
		templatePodID, templateContainerID := createTemplateContainer(ctx, t, client, getTemplatePodConfig(fmt.Sprintf("templatepod-%d", i)), getTemplateContainerConfig(fmt.Sprintf("templatecontainer-%d", i)))
		defer removePodSandbox(t, client, ctx, templatePodID)
		defer stopPodSandbox(t, client, ctx, templatePodID)
		defer removeContainer(t, client, ctx, templateContainerID)
		defer stopContainer(t, client, ctx, templateContainerID)
		templatePodIDs = append(templatePodIDs, templatePodID)
		templateContainerIDs = append(templateContainerIDs, templateContainerID)
	}

	clonedContainerIDs := []string{}
	for i := 0; i < nTemplates; i++ {
		clonedPodID, clonedContainerID := createClonedContainer(ctx, t, client, templatePodIDs[i], templateContainerIDs[i], i)
		defer removePodSandbox(t, client, ctx, clonedPodID)
		defer stopPodSandbox(t, client, ctx, clonedPodID)
		defer removeContainer(t, client, ctx, clonedContainerID)
		defer stopContainer(t, client, ctx, clonedContainerID)
		clonedContainerIDs = append(clonedContainerIDs, clonedContainerID)
	}

	for i := 0; i < nTemplates; i++ {
		verifyContainerExec(ctx, t, client, clonedContainerIDs[i])
	}
}