// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func tlsConfigWithAdditionalCA(caBundlePath, caPEM string) (*tls.Config, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12} // #nosec G402
	if caBundlePath == "" && caPEM == "" {
		return tlsConfig, nil
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system CA pool: %w", err)
	}
	if caBundlePath != "" {
		contents, readErr := os.ReadFile(caBundlePath)
		if readErr != nil {
			return nil, fmt.Errorf("read CA bundle: %w", readErr)
		}
		caPEM += "\n" + string(contents)
	}
	if !roots.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, fmt.Errorf("CA bundle contains no valid certificates")
	}
	tlsConfig.RootCAs = roots
	return tlsConfig, nil
}
