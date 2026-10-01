package cloud115

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const randCharset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// RandomString 生成指定长度的密码学安全随机字符串（PKCE code_verifier、
// OAuth state 等安全敏感场景）。必须使用 crypto/rand：math/rand 未播种时
// 序列可预测，会造成 PKCE 防御失效。
func RandomString(length int) string {
	b := make([]byte, length)
	max := big.NewInt(int64(len(randCharset)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			// 仅在系统熵源不可用时发生；静默降级为弱随机不可接受，直接暴露
			panic(fmt.Errorf("115: 生成安全随机字符串失败：%w", err))
		}
		b[i] = randCharset[n.Int64()]
	}
	return string(b)
}
