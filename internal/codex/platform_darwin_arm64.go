//go:build cgo

package codex

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo LDFLAGS: -framework CoreFoundation -framework Security
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdlib.h>
#include <string.h>

enum { keyLength = 32 };

static OSStatus retrieve_key(CFMutableDictionaryRef query, SecKeychainRef keychain, unsigned char *key) {
    const void *chains[] = {keychain};
    CFArrayRef search = CFArrayCreate(NULL, chains, 1, &kCFTypeArrayCallBacks);
    if (!search) return errSecAllocate;
    CFDictionarySetValue(query, kSecMatchSearchList, search);
    CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
    CFTypeRef value = NULL;
    OSStatus status = SecItemCopyMatching(query, &value);
    CFDictionaryRemoveValue(query, kSecReturnData);
    CFDictionaryRemoveValue(query, kSecMatchSearchList);
    CFRelease(search);
    if (status == errSecSuccess) {
        if (!value || CFGetTypeID(value) != CFDataGetTypeID() || CFDataGetLength(value) != keyLength) {
            status = errSecDecode;
        } else {
            memcpy(key, CFDataGetBytePtr(value), keyLength);
        }
    }
    if (value) CFRelease(value);
    return status;
}

static OSStatus create_key(CFMutableDictionaryRef query, SecKeychainRef keychain, unsigned char *key) {
    OSStatus status = SecRandomCopyBytes(kSecRandomDefault, keyLength, key);
    if (status != errSecSuccess) return status;
    CFDataRef secret = CFDataCreate(NULL, key, keyLength);
    if (!secret) return errSecAllocate;
    CFDictionarySetValue(query, kSecValueData, secret);
    CFDictionarySetValue(query, kSecUseKeychain, keychain);
    status = SecItemAdd(query, NULL);
    CFDictionaryRemoveValue(query, kSecValueData);
    CFDictionaryRemoveValue(query, kSecUseKeychain);
    CFRelease(secret);
    return status;
}

static OSStatus keychain_key(const char *path, const char *account, int create, unsigned char *key) {
    // File-based Keychain ACLs work for the same binary when launched either
    // as an app or as Codex's command helper, including local ad-hoc builds.
    SecKeychainRef keychain = NULL;
    OSStatus status = path ? SecKeychainOpen(path, &keychain)
                           : SecKeychainCopyDomainDefault(kSecPreferencesDomainUser, &keychain);
    if (status != errSecSuccess) return status;
    CFStringRef name = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
    CFMutableDictionaryRef query = CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks,
                                                              &kCFTypeDictionaryValueCallBacks);
    if (!query || !name) {
        status = errSecAllocate;
        goto done;
    }
    CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
    CFDictionarySetValue(query, kSecAttrService, CFSTR("ai.yungor.keydock-connect.credential-store.v1"));
    CFDictionarySetValue(query, kSecAttrAccount, name);
    status = retrieve_key(query, keychain, key);
    if (status == errSecItemNotFound && create) {
        status = create_key(query, keychain, key);
        if (status == errSecDuplicateItem) status = retrieve_key(query, keychain, key);
    }
done:
    if (query) CFRelease(query);
    if (name) CFRelease(name);
    CFRelease(keychain);
    return status;
}
*/
import "C"

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"unsafe"
)

type darwinKeychain string // Empty selects the user's default file-based Keychain.

const darwinProtectionVersion = "KDC1"

func ProtectForState(stateDir string, data []byte) ([]byte, error) {
	return darwinKeychain("").protect(stateDir)(data)
}

func UnprotectForState(stateDir string, data []byte) ([]byte, error) {
	return darwinKeychain("").unprotect(stateDir)(data)
}

func (chain darwinKeychain) protect(dir string) func([]byte) ([]byte, error) {
	return func(data []byte) ([]byte, error) {
		if len(data) == 0 {
			return nil, errors.New("缺少待加密资料")
		}
		aead, err := chain.cipher(dir, true)
		if err != nil {
			return nil, err
		}
		header := []byte(darwinProtectionVersion)
		return aead.Seal(bytes.Clone(header), nil, data, header), nil
	}
}

func (chain darwinKeychain) unprotect(dir string) func([]byte) ([]byte, error) {
	return func(data []byte) ([]byte, error) {
		header := []byte(darwinProtectionVersion)
		payload, ok := bytes.CutPrefix(data, header)
		if !ok {
			return nil, errors.New("本机加密资料格式无效")
		}
		aead, err := chain.cipher(dir, false)
		if err != nil {
			return nil, err
		}
		return aead.Open(nil, nil, payload, header)
	}
}

func (chain darwinKeychain) cipher(dir string, create bool) (cipher.AEAD, error) {
	if dir == "" {
		return nil, errors.New("缺少应用状态目录")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	var path *C.char
	if chain != "" {
		path = C.CString(string(chain))
		defer C.free(unsafe.Pointer(path))
	}
	sum := sha256.Sum256([]byte(abs))
	account := C.CString(hex.EncodeToString(sum[:]))
	defer C.free(unsafe.Pointer(account))
	var key [C.keyLength]byte
	defer clear(key[:])
	var allowCreate C.int
	if create {
		allowCreate = 1
	}
	status := C.keychain_key(path, account, allowCreate, (*C.uchar)(unsafe.Pointer(&key[0])))
	if status != C.errSecSuccess {
		return nil, fmt.Errorf("无法访问 macOS 钥匙串中的 KeyDock 凭据（%d）", int(status))
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}
