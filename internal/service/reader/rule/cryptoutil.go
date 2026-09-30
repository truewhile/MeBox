package rule

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/dop251/goja"
)

// 对称加密：对应 legado JsEncodeUtils.createSymmetricCrypto（hutool SymmetricCrypto 语义）。
// transformation 形如 "AES/CBC/PKCS5Padding" / "DES/ECB/NoPadding" / "DESede/CBC/PKCS5Padding"。

type symmetricCipher struct {
	block   cipher.Block
	mode    string
	padding string
	iv      []byte
}

func newSymmetricCipher(transformation, key, iv string) (*symmetricCipher, error) {
	parts := strings.Split(strings.TrimSpace(transformation), "/")
	algo := strings.ToUpper(strings.TrimSpace(parts[0]))
	mode := "CBC"
	padding := "PKCS5Padding"
	if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		mode = strings.ToUpper(strings.TrimSpace(parts[1]))
	}
	if len(parts) > 2 && strings.TrimSpace(parts[2]) != "" {
		padding = strings.TrimSpace(parts[2])
	}

	keyBytes := normalizeKey([]byte(key), algo)
	if len(keyBytes) == 0 {
		return nil, errors.New("加密密钥为空")
	}
	var block cipher.Block
	var err error
	switch algo {
	case "AES":
		block, err = aes.NewCipher(keyBytes)
	case "DES":
		block, err = des.NewCipher(keyBytes)
	case "DESEDE", "3DES", "TRIPLEDES":
		block, err = des.NewTripleDESCipher(keyBytes)
	default:
		return nil, fmt.Errorf("不支持的加密算法: %s", algo)
	}
	if err != nil {
		return nil, err
	}
	ivBytes := []byte(iv)
	if mode == "ECB" {
		ivBytes = nil
	}
	return &symmetricCipher{block: block, mode: mode, padding: padding, iv: ivBytes}, nil
}

// normalizeKey 对齐 hutool 的密钥处理：按算法要求补齐/截断密钥长度。
func normalizeKey(key []byte, algo string) []byte {
	var sizes []int
	switch algo {
	case "AES":
		sizes = []int{16, 24, 32}
	case "DES":
		sizes = []int{8}
	case "DESEDE", "3DES", "TRIPLEDES":
		sizes = []int{24}
	default:
		return key
	}
	for _, n := range sizes {
		if len(key) == n {
			return key
		}
	}
	for _, n := range sizes {
		if len(key) < n {
			out := make([]byte, n)
			copy(out, key)
			return out
		}
	}
	return key[:sizes[len(sizes)-1]]
}

func (c *symmetricCipher) blockSize() int { return c.block.BlockSize() }

func (c *symmetricCipher) crypt(dst, src []byte, decrypt bool) error {
	bs := c.blockSize()
	if c.mode == "ECB" {
		for i := 0; i < len(src); i += bs {
			if decrypt {
				c.block.Decrypt(dst[i:i+bs], src[i:i+bs])
			} else {
				c.block.Encrypt(dst[i:i+bs], src[i:i+bs])
			}
		}
		return nil
	}
	iv := make([]byte, bs)
	if len(c.iv) > 0 {
		copy(iv, c.iv[:min(len(c.iv), bs)])
	}
	switch c.mode {
	case "CBC":
		if decrypt {
			cipher.NewCBCDecrypter(c.block, iv).CryptBlocks(dst, src)
		} else {
			cipher.NewCBCEncrypter(c.block, iv).CryptBlocks(dst, src)
		}
	case "CTR":
		cipher.NewCTR(c.block, iv).XORKeyStream(dst, src)
	case "OFB":
		cipher.NewOFB(c.block, iv).XORKeyStream(dst, src)
	case "CFB", "CFB8":
		stream := cipher.NewCFBEncrypter(c.block, iv)
		if decrypt {
			stream = cipher.NewCFBDecrypter(c.block, iv)
		}
		stream.XORKeyStream(dst, src)
	default:
		return fmt.Errorf("不支持的加密模式: %s", c.mode)
	}
	return nil
}

func (c *symmetricCipher) encrypt(data []byte) ([]byte, error) {
	bs := c.blockSize()
	data = applyPadding(data, bs, c.padding)
	if len(data)%bs != 0 {
		return nil, errors.New("加密数据长度未按块对齐")
	}
	out := make([]byte, len(data))
	if err := c.crypt(out, data, false); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *symmetricCipher) decrypt(data []byte) ([]byte, error) {
	bs := c.blockSize()
	if len(data) == 0 || len(data)%bs != 0 {
		return nil, errors.New("密文长度不是块大小的整数倍")
	}
	out := make([]byte, len(data))
	if err := c.crypt(out, data, true); err != nil {
		return nil, err
	}
	return stripPadding(out, c.padding), nil
}

// decryptAuto 对应 hutool decryptStr(String)：输入自动识别 Base64 / Hex / 原文。
func (c *symmetricCipher) decryptAuto(s string) ([]byte, error) {
	if b, err := base64DecodeBytes(s); err == nil && len(b) > 0 && len(b)%c.blockSize() == 0 {
		return c.decrypt(b)
	}
	if b, err := hex.DecodeString(strings.TrimSpace(s)); err == nil && len(b) > 0 && len(b)%c.blockSize() == 0 {
		return c.decrypt(b)
	}
	return c.decrypt([]byte(s))
}

func applyPadding(data []byte, blockSize int, padding string) []byte {
	switch padding {
	case "NoPadding":
		return data
	case "ZeroPadding":
		n := (len(data)+blockSize-1)/blockSize*blockSize
		if n == 0 {
			n = blockSize
		}
		out := make([]byte, n)
		copy(out, data)
		return out
	default: // PKCS5Padding / PKCS7Padding
		pad := blockSize - len(data)%blockSize
		out := make([]byte, len(data)+pad)
		copy(out, data)
		for i := len(data); i < len(out); i++ {
			out[i] = byte(pad)
		}
		return out
	}
}

func stripPadding(data []byte, padding string) []byte {
	switch padding {
	case "NoPadding":
		return data
	case "ZeroPadding":
		for len(data) > 0 && data[len(data)-1] == 0 {
			data = data[:len(data)-1]
		}
		return data
	default:
		if len(data) == 0 {
			return data
		}
		pad := int(data[len(data)-1])
		if pad <= 0 || pad > len(data) {
			return data
		}
		for _, b := range data[len(data)-pad:] {
			if int(b) != pad {
				return data
			}
		}
		return data[:len(data)-pad]
	}
}

// newCipherObject 对应 hutool SymmetricCrypto 的 JS 方法面：
// encrypt(data) / encryptBase64(data) / encryptHex(data) / decrypt(data) /
// decryptStr(data) / decryptBase64(data) / decryptHex(data)。
func newCipherObject(vm *goja.Runtime, c *symmetricCipher) *goja.Object {
	o := vm.NewObject()
	set := func(k string, v any) {
		if err := o.Set(k, v); err != nil {
			panic(vm.ToValue(err.Error()))
		}
	}
	set("encrypt", func(call goja.FunctionCall) goja.Value {
		out, err := c.encrypt([]byte(stringArg(call, 0)))
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return vm.ToValue(vm.NewArrayBuffer(out))
	})
	set("encryptBase64", func(call goja.FunctionCall) goja.Value {
		out, err := c.encrypt([]byte(stringArg(call, 0)))
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return vm.ToValue(base64StdEncodeBytes(out))
	})
	set("encryptHex", func(call goja.FunctionCall) goja.Value {
		out, err := c.encrypt([]byte(stringArg(call, 0)))
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return vm.ToValue(hexEncodeBytes(out))
	})
	set("decrypt", func(call goja.FunctionCall) goja.Value {
		out, err := c.decryptAuto(stringArg(call, 0))
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return vm.ToValue(vm.NewArrayBuffer(out))
	})
	set("decryptStr", func(call goja.FunctionCall) goja.Value {
		out, err := c.decryptAuto(stringArg(call, 0))
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return vm.ToValue(string(out))
	})
	set("decryptBase64", func(call goja.FunctionCall) goja.Value {
		b, err := base64DecodeBytes(stringArg(call, 0))
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		out, err := c.decrypt(b)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return vm.ToValue(string(out))
	})
	set("decryptHex", func(call goja.FunctionCall) goja.Value {
		b, err := hexDecodeBytes(strings.TrimSpace(stringArg(call, 0)))
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		out, err := c.decrypt(b)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return vm.ToValue(string(out))
	})
	return o
}
