package envs

import (
	"os"
)

var UseFastMap = os.Getenv("SONIC_USE_FASTMAP") == "1"

func EnableFastMap() {
	UseFastMap = true
}

func DisableFastMap() {
	UseFastMap = false
}
