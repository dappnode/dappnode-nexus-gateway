package bootstrap

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
)

const (
	maxKMSRecipientBytes = 6 * 1024
	maxBERDepth          = 16
)

var (
	oidCMSData          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidCMSEnvelopedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 3}
	oidRSAESOAEP        = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 7}
	oidMGF1             = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 8}
	oidPSpecified       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 9}
	oidSHA256           = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidAES256CBC        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
)

type cmsContentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,tag:0"`
}

type cmsEnvelopedData struct {
	Version              int
	RecipientInfos       []cmsRecipientInfo `asn1:"set"`
	EncryptedContentInfo cmsEncryptedContentInfo
}

type cmsRecipientInfo struct {
	Version                int
	RecipientIdentifier    asn1.RawValue
	KeyEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedKey           []byte
}

type cmsEncryptedContentInfo struct {
	ContentType                asn1.ObjectIdentifier
	ContentEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedContent           asn1.RawValue `asn1:"tag:0,optional"`
}

type rsaOAEPParameters struct {
	HashAlgorithm    pkix.AlgorithmIdentifier `asn1:"explicit,optional,tag:0"`
	MaskGenAlgorithm pkix.AlgorithmIdentifier `asn1:"explicit,optional,tag:1"`
	PSourceAlgorithm pkix.AlgorithmIdentifier `asn1:"explicit,optional,tag:2"`
}

// decryptKMSRecipient implements the CMS unwrap defined for AWS KMS Nitro
// Recipient responses: RSAES-OAEP-SHA256 unwraps an AES-256-CBC content key,
// which decrypts the returned plaintext with PKCS#7 padding.
func decryptKMSRecipient(privateKey *rsa.PrivateKey, encoded []byte) ([]byte, error) {
	if privateKey == nil {
		return nil, errors.New("KMS recipient private key is unavailable")
	}
	if len(encoded) == 0 || len(encoded) > maxKMSRecipientBytes {
		return nil, errors.New("KMS recipient CMS response has an invalid size")
	}

	der, err := normalizeBER(encoded)
	if err != nil {
		return nil, fmt.Errorf("normalize KMS recipient CMS: %w", err)
	}
	var contentInfo cmsContentInfo
	if err := unmarshalExact(der, &contentInfo); err != nil {
		return nil, fmt.Errorf("decode KMS recipient content info: %w", err)
	}
	if !contentInfo.ContentType.Equal(oidCMSEnvelopedData) {
		return nil, errors.New("KMS recipient content is not CMS EnvelopedData")
	}

	var envelope cmsEnvelopedData
	if err := unmarshalExact(contentInfo.Content.Bytes, &envelope); err != nil {
		return nil, fmt.Errorf("decode KMS recipient envelope: %w", err)
	}
	if envelope.Version != 2 || len(envelope.RecipientInfos) != 1 {
		return nil, errors.New("KMS recipient envelope has an unsupported version or recipient count")
	}

	recipient := envelope.RecipientInfos[0]
	if recipient.Version != 2 || recipient.RecipientIdentifier.Class != asn1.ClassContextSpecific ||
		recipient.RecipientIdentifier.Tag != 0 || recipient.RecipientIdentifier.IsCompound ||
		len(recipient.RecipientIdentifier.Bytes) == 0 {
		return nil, errors.New("KMS recipient identifier is unsupported")
	}
	if !recipient.KeyEncryptionAlgorithm.Algorithm.Equal(oidRSAESOAEP) {
		return nil, errors.New("KMS recipient key algorithm is not RSAES-OAEP")
	}
	if err := validateOAEPParameters(recipient.KeyEncryptionAlgorithm.Parameters); err != nil {
		return nil, err
	}
	if len(recipient.EncryptedKey) != privateKey.Size() {
		return nil, errors.New("KMS recipient encrypted content key has an invalid size")
	}

	contentKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privateKey, recipient.EncryptedKey, nil)
	if err != nil {
		return nil, fmt.Errorf("unwrap KMS recipient content key: %w", err)
	}
	defer clear(contentKey)
	if len(contentKey) != 32 {
		return nil, errors.New("KMS recipient content key is not AES-256")
	}

	content := envelope.EncryptedContentInfo
	if !content.ContentType.Equal(oidCMSData) {
		return nil, errors.New("KMS recipient encrypted content is not CMS data")
	}
	if !content.ContentEncryptionAlgorithm.Algorithm.Equal(oidAES256CBC) {
		return nil, errors.New("KMS recipient content algorithm is not AES-256-CBC")
	}
	var iv []byte
	if err := unmarshalExact(content.ContentEncryptionAlgorithm.Parameters.FullBytes, &iv); err != nil || len(iv) != aes.BlockSize {
		return nil, errors.New("KMS recipient AES-CBC IV is invalid")
	}

	ciphertext, err := cmsEncryptedContent(content.EncryptedContent)
	if err != nil {
		return nil, err
	}
	return decryptAESCBC(contentKey, iv, ciphertext)
}

func validateOAEPParameters(raw asn1.RawValue) error {
	var parameters rsaOAEPParameters
	if len(raw.FullBytes) == 0 || unmarshalExact(raw.FullBytes, &parameters) != nil {
		return errors.New("KMS recipient RSAES-OAEP parameters are invalid")
	}
	if !parameters.HashAlgorithm.Algorithm.Equal(oidSHA256) || !isNullParameters(parameters.HashAlgorithm.Parameters) {
		return errors.New("KMS recipient OAEP hash is not SHA-256")
	}
	if !parameters.MaskGenAlgorithm.Algorithm.Equal(oidMGF1) {
		return errors.New("KMS recipient OAEP mask algorithm is not MGF1")
	}
	var maskHash pkix.AlgorithmIdentifier
	if unmarshalExact(parameters.MaskGenAlgorithm.Parameters.FullBytes, &maskHash) != nil ||
		!maskHash.Algorithm.Equal(oidSHA256) || !isNullParameters(maskHash.Parameters) {
		return errors.New("KMS recipient MGF1 hash is not SHA-256")
	}
	if len(parameters.PSourceAlgorithm.Algorithm) != 0 {
		if !parameters.PSourceAlgorithm.Algorithm.Equal(oidPSpecified) {
			return errors.New("KMS recipient OAEP label algorithm is unsupported")
		}
		var label []byte
		if unmarshalExact(parameters.PSourceAlgorithm.Parameters.FullBytes, &label) != nil || len(label) != 0 {
			return errors.New("KMS recipient OAEP label is not empty")
		}
	}
	return nil
}

func isNullParameters(raw asn1.RawValue) bool {
	return raw.Tag == asn1.TagNull && len(raw.Bytes) == 0
}

func cmsEncryptedContent(raw asn1.RawValue) ([]byte, error) {
	if len(raw.FullBytes) == 0 {
		return nil, errors.New("KMS recipient encrypted content is missing")
	}
	if !raw.IsCompound {
		if len(raw.Bytes) == 0 {
			return nil, errors.New("KMS recipient encrypted content is empty")
		}
		return append([]byte(nil), raw.Bytes...), nil
	}

	remaining := raw.Bytes
	var ciphertext []byte
	for len(remaining) != 0 {
		var part []byte
		rest, err := asn1.Unmarshal(remaining, &part)
		if err != nil || len(rest) == len(remaining) || len(part) == 0 {
			return nil, errors.New("KMS recipient encrypted content fragments are invalid")
		}
		if len(ciphertext)+len(part) > maxKMSRecipientBytes {
			return nil, errors.New("KMS recipient encrypted content is too large")
		}
		ciphertext = append(ciphertext, part...)
		remaining = rest
	}
	return ciphertext, nil
}

func decryptAESCBC(key, iv, ciphertext []byte) ([]byte, error) {
	if len(key) != 32 || len(iv) != aes.BlockSize || len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("KMS recipient AES-CBC inputs are invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("initialize KMS recipient content cipher")
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)

	padding := int(plaintext[len(plaintext)-1])
	valid := subtle.ConstantTimeLessOrEq(1, padding) & subtle.ConstantTimeLessOrEq(padding, aes.BlockSize)
	for offset := 0; offset < aes.BlockSize; offset++ {
		withinPadding := subtle.ConstantTimeLessOrEq(offset+1, padding)
		matches := subtle.ConstantTimeByteEq(plaintext[len(plaintext)-1-offset], byte(padding))
		valid &= subtle.ConstantTimeSelect(withinPadding, matches, 1)
	}
	if valid != 1 {
		clear(plaintext)
		return nil, errors.New("KMS recipient AES-CBC padding is invalid")
	}
	result := append([]byte(nil), plaintext[:len(plaintext)-padding]...)
	clear(plaintext)
	return result, nil
}

func unmarshalExact(encoded []byte, destination any) error {
	if len(encoded) == 0 {
		return errors.New("ASN.1 value is empty")
	}
	rest, err := asn1.Unmarshal(encoded, destination)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New("ASN.1 value has trailing data")
	}
	return nil
}

func normalizeBER(encoded []byte) ([]byte, error) {
	normalized, consumed, endOfContents, err := normalizeBERElement(encoded, 0)
	if err != nil {
		return nil, err
	}
	if endOfContents || consumed != len(encoded) {
		return nil, errors.New("BER value has trailing data or an unexpected terminator")
	}
	return normalized, nil
}

func normalizeBERElement(encoded []byte, depth int) ([]byte, int, bool, error) {
	if depth > maxBERDepth || len(encoded) < 2 {
		return nil, 0, false, errors.New("BER value is truncated or nested too deeply")
	}
	if encoded[0] == 0 && encoded[1] == 0 {
		return nil, 2, true, nil
	}

	position := 1
	if encoded[0]&0x1f == 0x1f {
		for count := 0; ; count++ {
			if position >= len(encoded) || count >= 4 {
				return nil, 0, false, errors.New("BER high-tag form is invalid")
			}
			current := encoded[position]
			position++
			if current&0x80 == 0 {
				break
			}
		}
	}
	tag := append([]byte(nil), encoded[:position]...)
	if position >= len(encoded) {
		return nil, 0, false, errors.New("BER length is missing")
	}

	lengthByte := encoded[position]
	position++
	indefinite := lengthByte == 0x80
	length := 0
	if !indefinite {
		if lengthByte&0x80 == 0 {
			length = int(lengthByte)
		} else {
			lengthBytes := int(lengthByte & 0x7f)
			if lengthBytes == 0 || lengthBytes > 4 || position+lengthBytes > len(encoded) || encoded[position] == 0 {
				return nil, 0, false, errors.New("BER long-form length is invalid")
			}
			for index := 0; index < lengthBytes; index++ {
				length = length<<8 | int(encoded[position+index])
			}
			position += lengthBytes
		}
		if length < 0 || length > maxKMSRecipientBytes || position+length > len(encoded) {
			return nil, 0, false, errors.New("BER content length is invalid")
		}
	}

	constructed := encoded[0]&0x20 != 0
	if indefinite && !constructed {
		return nil, 0, false, errors.New("BER primitive uses an indefinite length")
	}
	if !constructed {
		content := encoded[position : position+length]
		return encodeDERElement(tag, content), position + length, false, nil
	}

	contentLimit := len(encoded)
	if !indefinite {
		contentLimit = position + length
	}
	var content bytes.Buffer
	for position < contentLimit {
		child, consumed, endOfContents, err := normalizeBERElement(encoded[position:contentLimit], depth+1)
		if err != nil {
			return nil, 0, false, err
		}
		if endOfContents {
			if !indefinite {
				return nil, 0, false, errors.New("BER definite value contains an unexpected terminator")
			}
			position += consumed
			return encodeDERElement(tag, content.Bytes()), position, false, nil
		}
		if consumed <= 0 || content.Len()+len(child) > maxKMSRecipientBytes {
			return nil, 0, false, errors.New("BER constructed content is invalid or too large")
		}
		content.Write(child)
		position += consumed
	}
	if indefinite {
		return nil, 0, false, errors.New("BER indefinite value has no terminator")
	}
	return encodeDERElement(tag, content.Bytes()), position, false, nil
}

func encodeDERElement(tag, content []byte) []byte {
	encoded := make([]byte, 0, len(tag)+5+len(content))
	encoded = append(encoded, tag...)
	if len(content) < 128 {
		encoded = append(encoded, byte(len(content)))
	} else {
		length := len(content)
		var buffer [4]byte
		position := len(buffer)
		for length != 0 {
			position--
			buffer[position] = byte(length)
			length >>= 8
		}
		encoded = append(encoded, 0x80|byte(len(buffer)-position))
		encoded = append(encoded, buffer[position:]...)
	}
	encoded = append(encoded, content...)
	return encoded
}
