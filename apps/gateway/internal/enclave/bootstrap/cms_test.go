package bootstrap

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509/pkix"
	"encoding/asn1"
	"testing"
)

func TestDecryptKMSRecipient(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("0123456789abcdef0123456789abcdef")

	tests := []struct {
		name               string
		fragmentedContent  bool
		indefiniteEnvelope bool
	}{
		{name: "DER primitive content"},
		{name: "DER fragmented content", fragmentedContent: true},
		{name: "BER indefinite envelope", indefiniteEnvelope: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded := makeTestKMSRecipient(t, privateKey, plaintext, test.fragmentedContent)
			if test.indefiniteEnvelope {
				var outer asn1.RawValue
				if err := unmarshalExact(encoded, &outer); err != nil {
					t.Fatal(err)
				}
				encoded = append([]byte{0x30, 0x80}, outer.Bytes...)
				encoded = append(encoded, 0, 0)
			}

			decrypted, err := decryptKMSRecipient(privateKey, encoded)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(decrypted, plaintext) {
				t.Fatalf("plaintext = %x, want %x", decrypted, plaintext)
			}
		})
	}
}

func TestDecryptKMSRecipientRejectsUnexpectedAlgorithm(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded := makeTestKMSRecipient(t, privateKey, bytes.Repeat([]byte{0x42}, 32), false)

	var contentInfo cmsContentInfo
	if err := unmarshalExact(encoded, &contentInfo); err != nil {
		t.Fatal(err)
	}
	var envelope cmsEnvelopedData
	if err := unmarshalExact(contentInfo.Content.Bytes, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.EncryptedContentInfo.ContentEncryptionAlgorithm.Algorithm = asn1.ObjectIdentifier{1, 2, 3, 4}
	envelopeDER, err := asn1.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	encoded = wrapTestContentInfo(t, envelopeDER)

	if _, err := decryptKMSRecipient(privateKey, encoded); err == nil {
		t.Fatal("decryptKMSRecipient accepted an unexpected content algorithm")
	}
}

func TestDecryptAESCBCRejectsInvalidPadding(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	iv := bytes.Repeat([]byte{0x22}, aes.BlockSize)
	plaintext := bytes.Repeat([]byte{0x33}, aes.BlockSize)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, plaintext)

	if _, err := decryptAESCBC(key, iv, ciphertext); err == nil {
		t.Fatal("decryptAESCBC accepted invalid PKCS#7 padding")
	}
}

func makeTestKMSRecipient(t *testing.T, privateKey *rsa.PrivateKey, plaintext []byte, fragmented bool) []byte {
	t.Helper()

	contentKey := bytes.Repeat([]byte{0x44}, 32)
	iv := bytes.Repeat([]byte{0x55}, aes.BlockSize)
	block, err := aes.NewCipher(contentKey)
	if err != nil {
		t.Fatal(err)
	}
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := append(append([]byte(nil), plaintext...), bytes.Repeat([]byte{byte(padding)}, padding)...)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	encryptedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &privateKey.PublicKey, contentKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	null := asn1.RawValue{Tag: asn1.TagNull}
	sha256Algorithm := pkix.AlgorithmIdentifier{Algorithm: oidSHA256, Parameters: null}
	sha256AlgorithmDER, err := asn1.Marshal(sha256Algorithm)
	if err != nil {
		t.Fatal(err)
	}
	emptyLabelDER, err := asn1.Marshal([]byte{})
	if err != nil {
		t.Fatal(err)
	}
	oaepDER, err := asn1.Marshal(rsaOAEPParameters{
		HashAlgorithm: sha256Algorithm,
		MaskGenAlgorithm: pkix.AlgorithmIdentifier{
			Algorithm:  oidMGF1,
			Parameters: asn1.RawValue{FullBytes: sha256AlgorithmDER},
		},
		PSourceAlgorithm: pkix.AlgorithmIdentifier{
			Algorithm:  oidPSpecified,
			Parameters: asn1.RawValue{FullBytes: emptyLabelDER},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ivDER, err := asn1.Marshal(iv)
	if err != nil {
		t.Fatal(err)
	}

	encryptedContent := asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, Bytes: ciphertext}
	if fragmented {
		middle := len(ciphertext) / 2
		first, err := asn1.Marshal(ciphertext[:middle])
		if err != nil {
			t.Fatal(err)
		}
		second, err := asn1.Marshal(ciphertext[middle:])
		if err != nil {
			t.Fatal(err)
		}
		encryptedContent.IsCompound = true
		encryptedContent.Bytes = append(first, second...)
	}

	envelopeDER, err := asn1.Marshal(cmsEnvelopedData{
		Version: 2,
		RecipientInfos: []cmsRecipientInfo{{
			Version:             2,
			RecipientIdentifier: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, Bytes: bytes.Repeat([]byte{0x66}, 20)},
			KeyEncryptionAlgorithm: pkix.AlgorithmIdentifier{
				Algorithm:  oidRSAESOAEP,
				Parameters: asn1.RawValue{FullBytes: oaepDER},
			},
			EncryptedKey: encryptedKey,
		}},
		EncryptedContentInfo: cmsEncryptedContentInfo{
			ContentType: oidCMSData,
			ContentEncryptionAlgorithm: pkix.AlgorithmIdentifier{
				Algorithm:  oidAES256CBC,
				Parameters: asn1.RawValue{FullBytes: ivDER},
			},
			EncryptedContent: encryptedContent,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return wrapTestContentInfo(t, envelopeDER)
}

func wrapTestContentInfo(t *testing.T, envelopeDER []byte) []byte {
	t.Helper()
	oidDER, err := asn1.Marshal(oidCMSEnvelopedData)
	if err != nil {
		t.Fatal(err)
	}
	return encodeDERElement([]byte{0x30}, append(oidDER, encodeDERElement([]byte{0xa0}, envelopeDER)...))
}
