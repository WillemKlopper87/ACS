package firmware

import "acs/internal/objstore"

// Storage is the firmware object store: local disk by default, S3 when
// ACS_OBJECT_STORE=s3 (see internal/objstore). One object per image,
// named by its Repository ID so the DB row and the object are always
// found the same way.
type Storage = objstore.Store

// NewStorage returns the local-disk backend rooted at root; main() uses
// objstore.FromEnv to pick the backend from the environment.
func NewStorage(root string) (Storage, error) {
	return objstore.NewLocal(root)
}
