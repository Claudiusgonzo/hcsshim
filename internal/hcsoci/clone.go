// +build windows

package hcsoci

import (
	"context"
	"fmt"

	"github.com/Microsoft/hcsshim/internal/cow"
	"github.com/Microsoft/hcsshim/internal/requesttype"
	hcsschema "github.com/Microsoft/hcsshim/internal/schema2"
)

const (
	MappedDirectoryResourcePath = "Container/MappedDirectories"
	MappedPipeResourcePath      = "Container/MappedPipes"
)

// Usually mounts specified in the container config are added in the container doc
// that is passed along with the container creation reuqest. However, for cloned containers
// we don't send any create container request so we must add the mounts one by one by
// doing Modify requests to that container.
func addMountsToClone(ctx context.Context, c cow.Container, mounts *mountsConfig) error {
	// TODO(ambarve) : Find out if there is a way to send request for all the mounts
	// at the same time to save time
	for _, md := range mounts.mdsv2 {
		requestDocument := &hcsschema.ModifySettingRequest{
			RequestType:  requesttype.Add,
			ResourcePath: MappedDirectoryResourcePath,
			Settings:     md,
		}
		err := c.Modify(ctx, requestDocument)
		if err != nil {
			return fmt.Errorf("Error while adding mapped directory (%s) to the container: %s", md.HostPath, err)
		}
	}

	for _, mp := range mounts.mpsv2 {
		requestDocument := &hcsschema.ModifySettingRequest{
			RequestType:  requesttype.Add,
			ResourcePath: MappedPipeResourcePath,
			Settings:     mp,
		}
		err := c.Modify(ctx, requestDocument)
		if err != nil {
			return fmt.Errorf("Error while adding mapped pipe (%s) to the container: %s", mp.HostPath, err)
		}
	}
	return nil
}
