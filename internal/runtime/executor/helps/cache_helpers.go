package helps

import (
	"time"
)

// CodexCache is a prompt cache identity with its expiry.
type CodexCache struct {
	ID     string
	Expire time.Time
}
