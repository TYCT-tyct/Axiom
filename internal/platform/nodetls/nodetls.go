// Package nodetls builds the mutual-TLS configuration for calls between Axiom nodes. The network between nodes is not
// trusted to say who a peer is: every node presents a certificate and verifies the other's against the deployment's
// internal certificate authority. The files live in the node's protected key directory, next to the key ring: node.crt
// and node.key are this node's pair, ca.crt is the authority that signed every node's certificate. Issuing, registering
// and rotating them is the deployment's job for now (docs/architecture.md「部署拓扑」); this package only refuses to
// start with material it cannot use.
package nodetls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"
)

// The variable that names the directory and the files it must hold. Their names are the contract with the deployment and
// with backup and restore. Exported so docs and tests name the same strings.
const (
	Variable = "AXIOM_NODE_TLS_DIR"
	CertFile = "node.crt"
	KeyFile  = "node.key"
	CAFile   = "ca.crt"
)

// Server is the configuration a node accepts its peers with: this node's certificate, and a client certificate signed
// by the same authority required from every peer.
func Server(dir string) (*tls.Config, error) {
	cert, pool, err := load(dir)
	if err != nil {
		return nil, err
	}
	if !allows(cert.Leaf, x509.ExtKeyUsageServerAuth) {
		return nil, fmt.Errorf("%s: %s is not a server certificate", Variable, CertFile)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// Client is the configuration a node calls a peer with: this node's certificate, the authority that signed the peer's
// certificate, and the name the peer's certificate must carry.
func Client(dir, serverName string) (*tls.Config, error) {
	cert, pool, err := load(dir)
	if err != nil {
		return nil, err
	}
	if !allows(cert.Leaf, x509.ExtKeyUsageClientAuth) {
		return nil, fmt.Errorf("%s: %s is not a client certificate", Variable, CertFile)
	}
	if serverName == "" {
		return nil, fmt.Errorf("the core URL names no host to verify")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// load reads one node's certificate, key and authority. Every error names the variable and the file's role, never the
// directory, a path or a byte of the material; the key file must be private like the key ring, because it is what lets
// a process act as this node.
func load(dir string) (tls.Certificate, *x509.CertPool, error) {
	certPEM, keyPEM, caPEM, err := readNodeFiles(dir)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return parseNodeFiles(certPEM, keyPEM, caPEM)
}

func readNodeFiles(dir string) (certPEM, keyPEM, caPEM []byte, err error) {
	if dir == "" {
		return nil, nil, nil, fmt.Errorf("%s: required", Variable)
	}
	if err = protectedDir(dir); err != nil {
		return nil, nil, nil, err
	}
	if certPEM, err = read(dir, CertFile); err != nil {
		return nil, nil, nil, err
	}
	if err = private(dir, KeyFile); err != nil {
		return nil, nil, nil, err
	}
	if keyPEM, err = read(dir, KeyFile); err != nil {
		return nil, nil, nil, err
	}
	if caPEM, err = read(dir, CAFile); err != nil {
		return nil, nil, nil, err
	}
	return certPEM, keyPEM, caPEM, nil
}

func parseNodeFiles(certPEM, keyPEM, caPEM []byte) (tls.Certificate, *x509.CertPool, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("%s: %s and %s are not a matching certificate and key", Variable, CertFile, KeyFile)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("%s: %s is not a certificate", Variable, CertFile)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return tls.Certificate{}, nil, fmt.Errorf("%s: %s is not valid now", Variable, CertFile)
	}
	cert.Leaf = leaf
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return tls.Certificate{}, nil, fmt.Errorf("%s: %s holds no certificate", Variable, CAFile)
	}
	if err := verifyNodeCertificate(cert, leaf, pool); err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("%s: %s is not signed by %s", Variable, CertFile, CAFile)
	}
	return cert, pool, nil
}

// verifyNodeCertificate checks that this node's certificate belongs to the configured authority, so a node cannot start
// with a pair its peers will refuse. The certificate may include its intermediates; the authority is the root.
func verifyNodeCertificate(cert tls.Certificate, leaf *x509.Certificate, pool *x509.CertPool) error {
	intermediates := x509.NewCertPool()
	for _, der := range cert.Certificate[1:] {
		if parsed, err := x509.ParseCertificate(der); err == nil {
			intermediates.AddCert(parsed)
		}
	}
	_, err := leaf.Verify(x509.VerifyOptions{
		Roots: pool, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	return err
}

// allows reports whether a leaf may be used for the given purpose. A certificate without an extended key usage is
// valid for every purpose, as X.509 says; one that names usages must name this one.
func allows(leaf *x509.Certificate, usage x509.ExtKeyUsage) bool {
	return len(leaf.ExtKeyUsage) == 0 || slices.Contains(leaf.ExtKeyUsage, usage)
}

// protectedDir refuses a directory group or others can write: whoever can write it can replace the key this node
// presents, even though the key file itself is private. Read and traverse are allowed, as the deployment's mount may
// need them.
func protectedDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // read names the missing file, which is more useful than the directory
		}
		return fmt.Errorf("%s: %s", Variable, reason(err))
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: not a directory", Variable)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s: the certificate directory must not be writable by group or others", Variable)
	}
	return nil
}

// private refuses a key file group or others can read, the same rule the key ring's files follow. Windows mode bits
// cannot express NTFS ACLs, so there the deployment restricts the file instead.
func private(dir, name string) error {
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		return fmt.Errorf("%s: %s: %s", Variable, name, reason(err))
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s: %s permissions must deny all group and other access", Variable, name)
	}
	return nil
}

func read(dir, name string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- the trusted startup setting chooses the directory.
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %s", Variable, name, reason(err))
	}
	return data, nil
}

func reason(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "file not found"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	}
	return "failed"
}
