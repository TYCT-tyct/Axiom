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
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"axiom/internal/platform/privfile"
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
	cert, pool, err := load(dir, x509.ExtKeyUsageServerAuth)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// OpsPurpose is the URI a node certificate names among its subject alternative names when it may call the operations
// listener: the ops node's, for its MCP bridge. Any certificate the authority signed opens the node listener; only
// one that carries this purpose opens the operations listener, so an ingress's certificate cannot reach the
// operations API.
const OpsPurpose = "urn:axiom:node-purpose:ops"

// ErrPurpose is a peer whose verified certificate does not carry the purpose a listener requires.
var ErrPurpose = errors.New("the client certificate does not carry the purpose this listener requires")

// RequirePurpose is server, which must require and verify client certificates, also refusing a peer whose certificate
// does not carry purpose. The check runs after the chain is verified, on the peer's own certificate.
func RequirePurpose(server *tls.Config, purpose string) *tls.Config {
	cfg := server.Clone()
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return ErrPurpose
		}
		for _, uri := range cs.PeerCertificates[0].URIs {
			if uri.String() == purpose {
				return nil
			}
		}
		return ErrPurpose
	}
	return cfg
}

// Client is the configuration a node calls a peer with: this node's certificate, the authority that signed the peer's
// certificate, and the name the peer's certificate must carry.
func Client(dir, serverName string) (*tls.Config, error) {
	cert, pool, err := load(dir, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return nil, err
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
func load(dir string, usage x509.ExtKeyUsage) (tls.Certificate, *x509.CertPool, error) {
	return loadWith(dir, usage, os.Open)
}

// loadWith is load with the open as a parameter. A test uses it to put another key at the path as the open happens, so
// the ordering that keeps a replaced key from being read is proven every run instead of raced against a writer.
func loadWith(dir string, usage x509.ExtKeyUsage, open privfile.Opener) (tls.Certificate, *x509.CertPool, error) {
	certPEM, keyPEM, caPEM, err := readNodeFiles(dir, open)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return parseNodeFiles(certPEM, keyPEM, caPEM, usage)
}

func readNodeFiles(dir string, open privfile.Opener) (certPEM, keyPEM, caPEM []byte, err error) {
	if dir == "" {
		return nil, nil, nil, fmt.Errorf("%s: required", Variable)
	}
	if err = protectedDir(dir); err != nil {
		return nil, nil, nil, err
	}
	if certPEM, err = read(dir, CertFile); err != nil {
		return nil, nil, nil, err
	}
	if keyPEM, err = readPrivate(dir, KeyFile, open); err != nil {
		return nil, nil, nil, err
	}
	if caPEM, err = read(dir, CAFile); err != nil {
		return nil, nil, nil, err
	}
	return certPEM, keyPEM, caPEM, nil
}

func parseNodeFiles(certPEM, keyPEM, caPEM []byte, usage x509.ExtKeyUsage) (tls.Certificate, *x509.CertPool, error) {
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
	if err := verifyNodeCertificate(cert, leaf, pool, usage); err != nil {
		return tls.Certificate{}, nil, unusable(usage, err)
	}
	return cert, pool, nil
}

// unusable says why the pair cannot serve its purpose without printing a path or certificate detail. A chain whose
// extended key usage does not allow this purpose (a CA signed only for serverAuth, for example) is a wrong-purpose
// error, not an unknown authority; the full verification, not a separate check of the leaf, is what finds it.
func unusable(usage x509.ExtKeyUsage, err error) error {
	if invalid, ok := errors.AsType[x509.CertificateInvalidError](err); ok {
		if invalid.Reason == x509.IncompatibleUsage {
			kind := "server"
			if usage == x509.ExtKeyUsageClientAuth {
				kind = "client"
			}
			return fmt.Errorf("%s: %s is not a %s certificate", Variable, CertFile, kind)
		}
		if invalid.Reason == x509.Expired {
			return fmt.Errorf("%s: a certificate in the chain is not valid now", Variable)
		}
	}
	return fmt.Errorf("%s: %s is not signed by %s", Variable, CertFile, CAFile)
}

// verifyNodeCertificate checks that this node's certificate belongs to the configured authority, so a node cannot start
// with a pair its peers will refuse. The certificate may include its intermediates; the authority is the root.
func verifyNodeCertificate(cert tls.Certificate, leaf *x509.Certificate, pool *x509.CertPool, usage x509.ExtKeyUsage) error {
	intermediates := x509.NewCertPool()
	for _, der := range cert.Certificate[1:] {
		if parsed, err := x509.ParseCertificate(der); err == nil {
			intermediates.AddCert(parsed)
		}
	}
	_, err := leaf.Verify(x509.VerifyOptions{
		Roots: pool, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{usage},
	})
	return err
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

// readPrivate reads the key that lets this process act as the node. Its type and mode are checked on the open
// descriptor that is then read, as the key ring's are, so a path replaced after the check cannot become the key the
// node presents. Windows mode bits cannot express NTFS ACLs, so there the deployment restricts the file instead.
func readPrivate(dir, name string, open privfile.Opener) ([]byte, error) {
	file, err := privfile.Open(filepath.Join(dir, name), open)
	if err != nil {
		if errors.Is(err, privfile.ErrNotRegular) || errors.Is(err, privfile.ErrPermissions) {
			return nil, fmt.Errorf("%s: %s %w", Variable, name, err)
		}
		return nil, fmt.Errorf("%s: %s: %s", Variable, name, reason(err))
	}
	defer file.Close() //nolint:errcheck // A file opened only for reading has nothing to flush.
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %s", Variable, name, reason(err))
	}
	return data, nil
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
