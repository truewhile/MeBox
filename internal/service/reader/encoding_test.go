package reader

import "encoding/base64"

func base64EncodeStr(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
