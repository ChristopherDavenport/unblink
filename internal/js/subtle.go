package js

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"hash"

	"github.com/dop251/goja"
)

// crypto.subtle is implemented as a set of pure byte->byte native primitives
// (this file) with the WebCrypto object model — Promises, CryptoKey, formats,
// algorithm normalization — living in JS (preludeAPIJS). The primitives are
// synchronous CPU work (no off-loop I/O), so the JS layer resolves immediately;
// nothing here touches the settle audit. ADR 0006 records the reversal of the
// earlier "leave crypto.subtle undefined" decision and its non-goals (RSA/ECDSA).
//
// Randomness comes from crypto/rand, so keys generated here are real; the JS
// layer also re-points crypto.getRandomValues at __unblinkRandomBytes.
func (b *bridge) installSubtle() {
	vm := b.vm
	set := func(name string, fn func(goja.FunctionCall) goja.Value) { _ = vm.Set(name, fn) }

	set("__unblinkRandomBytes", func(call goja.FunctionCall) goja.Value {
		n := int(call.Argument(0).ToInteger())
		if n < 0 || n > 1<<20 { // cap at 1 MiB per draw
			panic(vm.NewTypeError("QuotaExceededError: requested too many random bytes"))
		}
		buf := make([]byte, n)
		if _, err := rand.Read(buf); err != nil {
			panic(vm.NewTypeError("OperationError: %s", err.Error()))
		}
		return vm.ToValue(vm.NewArrayBuffer(buf))
	})

	set("__unblinkDigest", func(call goja.FunctionCall) goja.Value {
		h := subtleHash(vm, call.Argument(0).String())
		hh := h()
		hh.Write(abBytes(call.Argument(1)))
		return vm.ToValue(vm.NewArrayBuffer(hh.Sum(nil)))
	})

	set("__unblinkHmacSign", func(call goja.FunctionCall) goja.Value {
		h := subtleHash(vm, call.Argument(0).String())
		mac := hmac.New(h, abBytes(call.Argument(1)))
		mac.Write(abBytes(call.Argument(2)))
		return vm.ToValue(vm.NewArrayBuffer(mac.Sum(nil)))
	})

	set("__unblinkAesEncrypt", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(vm.NewArrayBuffer(subtleAES(vm, true,
			call.Argument(0).String(), abBytes(call.Argument(1)), abBytes(call.Argument(2)),
			abBytes(call.Argument(3)), abBytes(call.Argument(4)))))
	})
	set("__unblinkAesDecrypt", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(vm.NewArrayBuffer(subtleAES(vm, false,
			call.Argument(0).String(), abBytes(call.Argument(1)), abBytes(call.Argument(2)),
			abBytes(call.Argument(3)), abBytes(call.Argument(4)))))
	})

	set("__unblinkPbkdf2", func(call goja.FunctionCall) goja.Value {
		h := subtleHash(vm, call.Argument(0).String())
		iter := int(call.Argument(3).ToInteger())
		bits := int(call.Argument(4).ToInteger())
		// A Go native can't be vm.Interrupt-ed mid-loop, so bound the two
		// untrusted-input DoS vectors: iteration count and derived length.
		if iter < 1 || iter > maxPBKDF2Iter {
			panic(vm.NewTypeError("NotSupportedError: PBKDF2 iteration count out of range"))
		}
		if bits < 0 || bits/8 > maxDerivedBytes {
			panic(vm.NewTypeError("OperationError: PBKDF2 derived length too large"))
		}
		out := pbkdf2Key(h, abBytes(call.Argument(1)), abBytes(call.Argument(2)), iter, bits/8)
		return vm.ToValue(vm.NewArrayBuffer(out))
	})
	set("__unblinkHkdf", func(call goja.FunctionCall) goja.Value {
		h := subtleHash(vm, call.Argument(0).String())
		bits := int(call.Argument(4).ToInteger())
		if bits < 0 || bits/8 > maxDerivedBytes {
			panic(vm.NewTypeError("OperationError: HKDF derived length too large"))
		}
		out, err := hkdfKey(h, abBytes(call.Argument(1)), abBytes(call.Argument(2)), abBytes(call.Argument(3)), bits/8)
		if err != nil {
			panic(vm.NewTypeError("OperationError: %s", err.Error()))
		}
		return vm.ToValue(vm.NewArrayBuffer(out))
	})
}

// Bounds on untrusted crypto inputs (CPU/memory DoS guards). maxPBKDF2Iter sits
// well above legitimate use (~600k) but far below a wall-clock stall; the memory
// guard (ADR 0003) covers digest/HMAC/AES input size separately.
const (
	maxPBKDF2Iter   = 10_000_000
	maxDerivedBytes = 1 << 20 // 1 MiB of derived key material
)

// subtleHash maps a WebCrypto hash name to a hash.Hash constructor.
func subtleHash(vm *goja.Runtime, name string) func() hash.Hash {
	switch name {
	case "SHA-1":
		return sha1.New
	case "SHA-256":
		return sha256.New
	case "SHA-384":
		return sha512.New384
	case "SHA-512":
		return sha512.New
	}
	panic(vm.NewTypeError("NotSupportedError: unsupported hash %q", name))
}

// abBytes extracts the bytes from a goja ArrayBuffer argument (the JS layer
// always passes ArrayBuffers), or nil for null/undefined.
func abBytes(v goja.Value) []byte {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	switch t := v.Export().(type) {
	case goja.ArrayBuffer:
		return t.Bytes()
	case []byte:
		return t
	}
	return nil
}

// subtleAES runs AES-GCM / AES-CBC / AES-CTR in the given direction. GCM returns
// ciphertext||tag (Web Crypto layout); CBC uses PKCS#7 padding; CTR is symmetric.
func subtleAES(vm *goja.Runtime, encrypt bool, mode string, key, iv, data, aad []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(vm.NewTypeError("OperationError: %s", err.Error()))
	}
	switch mode {
	case "GCM":
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			panic(vm.NewTypeError("OperationError: %s", err.Error()))
		}
		if len(iv) == 0 {
			panic(vm.NewTypeError("OperationError: AES-GCM requires an iv"))
		}
		if encrypt {
			return gcm.Seal(nil, iv, data, aad)
		}
		out, err := gcm.Open(nil, iv, data, aad)
		if err != nil {
			panic(vm.NewTypeError("OperationError: AES-GCM authentication failed"))
		}
		return out
	case "CBC":
		if len(iv) != aes.BlockSize {
			panic(vm.NewTypeError("OperationError: AES-CBC requires a 16-byte iv"))
		}
		if encrypt {
			padded := pkcs7Pad(data, aes.BlockSize)
			out := make([]byte, len(padded))
			cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
			return out
		}
		if len(data) == 0 || len(data)%aes.BlockSize != 0 {
			panic(vm.NewTypeError("OperationError: invalid AES-CBC ciphertext length"))
		}
		out := make([]byte, len(data))
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
		unpadded, err := pkcs7Unpad(out, aes.BlockSize)
		if err != nil {
			panic(vm.NewTypeError("OperationError: %s", err.Error()))
		}
		return unpadded
	case "CTR":
		if len(iv) != aes.BlockSize {
			panic(vm.NewTypeError("OperationError: AES-CTR requires a 16-byte counter"))
		}
		out := make([]byte, len(data))
		cipher.NewCTR(block, iv).XORKeyStream(out, data)
		return out
	}
	panic(vm.NewTypeError("NotSupportedError: unsupported AES mode %q", mode))
}

// pbkdf2Key is RFC 2898 PBKDF2 over HMAC (stdlib crypto/hmac; no external dep).
func pbkdf2Key(h func() hash.Hash, password, salt []byte, iter, keyLen int) []byte {
	if iter < 1 {
		iter = 1
	}
	prf := hmac.New(h, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen
	dk := make([]byte, 0, numBlocks*hashLen)
	buf := make([]byte, 4)
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf, uint32(block))
		prf.Write(buf)
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for x := range t {
				t[x] ^= u[x]
			}
		}
		dk = append(dk, t...)
	}
	if keyLen > len(dk) {
		keyLen = len(dk)
	}
	return dk[:keyLen]
}

// hkdfKey is RFC 5869 HKDF (extract-then-expand) over HMAC.
func hkdfKey(h func() hash.Hash, secret, salt, info []byte, keyLen int) ([]byte, error) {
	hashLen := h().Size()
	if len(salt) == 0 {
		salt = make([]byte, hashLen)
	}
	ext := hmac.New(h, salt)
	ext.Write(secret)
	prk := ext.Sum(nil)
	if keyLen > 255*hashLen {
		return nil, fmt.Errorf("HKDF key length too large")
	}
	n := (keyLen + hashLen - 1) / hashLen
	okm := make([]byte, 0, n*hashLen)
	var t []byte
	for i := 1; i <= n; i++ {
		exp := hmac.New(h, prk)
		exp.Write(t)
		exp.Write(info)
		exp.Write([]byte{byte(i)})
		t = exp.Sum(nil)
		okm = append(okm, t...)
	}
	return okm[:keyLen], nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, fmt.Errorf("invalid PKCS#7 padding")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, fmt.Errorf("invalid PKCS#7 padding")
	}
	for i := len(data) - pad; i < len(data); i++ {
		if int(data[i]) != pad {
			return nil, fmt.Errorf("invalid PKCS#7 padding")
		}
	}
	return data[:len(data)-pad], nil
}
