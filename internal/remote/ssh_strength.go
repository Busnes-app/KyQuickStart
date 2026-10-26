package remote

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"errors"
	"slices"

	"golang.org/x/crypto/ssh"
)

func strongSSHKey(key ssh.PublicKey) error {
	if cert, ok := key.(*ssh.Certificate); ok {
		if cert.Signature == nil || !slices.Contains(ssh.SupportedAlgorithms().PublicKeyAuths, cert.Signature.Format) {
			return errors.New("SSH certificate needs a supported signing algorithm")
		}
		if err := strongSSHRawKey(cert.SignatureKey); err != nil {
			return err
		}
		key = cert.Key
	}
	return strongSSHRawKey(key)
}

func strongSSHRawKey(key ssh.PublicKey) error {
	if public, ok := key.(ssh.CryptoPublicKey); ok {
		switch k := public.CryptoPublicKey().(type) {
		case *rsa.PublicKey:
			if k.N.BitLen() >= 2048 {
				return nil
			}
		case *ecdsa.PublicKey:
			if k.Curve.Params().BitSize >= 224 {
				return nil
			}
		case ed25519.PublicKey:
			if len(k) == ed25519.PublicKeySize {
				return nil
			}
		}
	}
	return errors.New("unsupported SSH key: use Ed25519, ECDSA or RSA >=2048; rotate the key without deleting existing access")
}

func strongSSHSigner(signer ssh.Signer) (ssh.Signer, error) {
	// Agent identities expose wire blobs, not CryptoPublicKey implementations.
	key, err := ssh.ParsePublicKey(signer.PublicKey().Marshal())
	if err != nil {
		return nil, err
	}
	if key.Type() != signer.PublicKey().Type() {
		return nil, errors.New("SSH agent key format disagrees with its public key bytes")
	}
	if err := strongSSHKey(key); err != nil {
		return nil, err
	}
	if cert, ok := key.(*ssh.Certificate); ok {
		key = cert.Key
	}
	if key.Type() != ssh.KeyAlgoRSA {
		return signer, nil
	}
	as, ok := signer.(ssh.AlgorithmSigner)
	if !ok {
		return nil, errors.New("RSA SSH key needs SHA-2 signing support")
	}
	algorithms := []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	if multi, ok := signer.(ssh.MultiAlgorithmSigner); ok {
		algorithms = slices.DeleteFunc(algorithms, func(a string) bool { return !slices.Contains(multi.Algorithms(), a) })
	}
	return ssh.NewSignerWithAlgorithms(as, algorithms)
}
