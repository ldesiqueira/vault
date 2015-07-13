package cassandra

import (
	"fmt"

	"github.com/fatih/structs"
	"github.com/hashicorp/vault/helper/certutil"
	"github.com/hashicorp/vault/logical"
	"github.com/hashicorp/vault/logical/framework"
)

func pathConfigConnection(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "config/connection",
		Fields: map[string]*framework.FieldSchema{
			"hosts": &framework.FieldSchema{
				Type:        framework.TypeString,
				Description: "Comma-separated list of hosts",
			},

			"username": &framework.FieldSchema{
				Type:        framework.TypeString,
				Description: "The username to use for connecting to the cluster",
			},

			"password": &framework.FieldSchema{
				Type:        framework.TypeString,
				Description: "The password to use for connecting to the cluster",
			},

			"tls": &framework.FieldSchema{
				Type: framework.TypeBool,
				Description: `Whether to use TLS. If pem_bundle or pem_json are
set, this is automatically set to true`,
			},

			"insecure_tls": &framework.FieldSchema{
				Type: framework.TypeBool,
				Description: `Whether to use TLS but skip verification; has no
effect if a CA certificate is provided`,
			},

			"pem_bundle": &framework.FieldSchema{
				Type: framework.TypeString,
				Description: `PEM-format, concatenated unencrypted secret key
and certificate, with optional CA certificate`,
			},

			"pem_json": &framework.FieldSchema{
				Type: framework.TypeString,
				Description: `JSON containing a PEM-format, unencrypted secret
key and certificate, with optional CA certificate.
The JSON output of a certificate issued with the PKI
backend can be directly passed into this parameter.
If both this and "pem_bundle" are specified, this will
take precedence.`,
			},
		},

		Callbacks: map[logical.Operation]framework.OperationFunc{
			logical.ReadOperation:  b.pathConnectionRead,
			logical.WriteOperation: b.pathConnectionWrite,
		},

		HelpSynopsis:    pathConfigConnectionHelpSyn,
		HelpDescription: pathConfigConnectionHelpDesc,
	}
}

func (b *backend) pathConnectionRead(
	req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	entry, err := req.Storage.Get("config/connection")
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return logical.ErrorResponse(fmt.Sprintf("Configure the DB connection with config/connection first")), nil
	}

	config := &sessionConfig{}
	if err := entry.DecodeJSON(config); err != nil {
		return nil, err
	}

	config.Password = "**********"
	if len(config.PrivateKey) > 0 {
		config.PrivateKey = "**********"
	}

	return &logical.Response{
		Data: structs.New(config).Map(),
	}, nil
}

func (b *backend) pathConnectionWrite(
	req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	hosts := data.Get("hosts").(string)
	username := data.Get("username").(string)
	password := data.Get("password").(string)

	switch {
	case len(hosts) == 0:
		return logical.ErrorResponse("Hosts cannot be empty"), nil
	case len(username) == 0:
		return logical.ErrorResponse("Username cannot be empty"), nil
	case len(password) == 0:
		return logical.ErrorResponse("Password cannot be empty"), nil
	}

	config := &sessionConfig{
		Hosts:       hosts,
		Username:    username,
		Password:    password,
		TLS:         data.Get("tls").(bool),
		InsecureTLS: data.Get("insecure_tls").(bool),
	}

	if config.InsecureTLS {
		config.TLS = true
	}

	pemBundle := data.Get("pem_bundle").(string)
	pemJSON := data.Get("pem_json").(string)

	var certBundle *certutil.CertBundle
	var parsedCertBundle *certutil.ParsedCertBundle
	var err error

	switch {
	case len(pemJSON) != 0:
		parsedCertBundle, err = certutil.ParsePKIJSON([]byte(pemJSON))
		if err != nil {
			return logical.ErrorResponse(fmt.Sprintf("Could not parse given JSON; it must be in the format of the output of the PKI backend certificate issuing command: %s", err)), nil
		}
		certBundle, err = parsedCertBundle.ToCertBundle()
		if err != nil {
			return logical.ErrorResponse(fmt.Sprintf("Error marshaling PEM information: %s", err)), nil
		}
		config.Certificate = certBundle.Certificate
		config.PrivateKey = certBundle.PrivateKey
		config.IssuingCA = certBundle.IssuingCA
		config.TLS = true

	case len(pemBundle) != 0:
		parsedCertBundle, err = certutil.ParsePEMBundle(pemBundle)
		if err != nil {
			return logical.ErrorResponse(fmt.Sprintf("Error parsing the given PEM information: %s", err)), nil
		}
		certBundle, err = parsedCertBundle.ToCertBundle()
		if err != nil {
			return logical.ErrorResponse(fmt.Sprintf("Error marshaling PEM information: %s", err)), nil
		}
		config.Certificate = certBundle.Certificate
		config.PrivateKey = certBundle.PrivateKey
		config.IssuingCA = certBundle.IssuingCA
		config.TLS = true
	}

	session, err := createSession(config, req.Storage)
	if err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}

	// Store it
	entry, err := logical.StorageEntryJSON("config/connection", config)
	if err != nil {
		return nil, err
	}
	if err := req.Storage.Put(entry); err != nil {
		return nil, err
	}

	// Reset the DB connection
	b.ResetDB(session)

	return nil, nil
}

const pathConfigConnectionHelpSyn = `
Configure the connection information to talk to Cassandra.
`

const pathConfigConnectionHelpDesc = `
This path configures the connection information used to connect to Cassandra.

"hosts" is a comma-deliniated list of hostnames in the Cassandra cluster.

"username" and "password" are self-explanatory, although the given user
must have superuser access within Cassandra. Note that since this backend
issues username/password credentials, Cassandra must be configured to use
PasswordAuthenticator or a similar backend for its authentication. If you wish
to have no authorization in Cassandra and want to use TLS client certificates,
see the PKI backend.

TLS works as follows:

* If "tls" is set to true, the connection will use TLS; this happens automatically if "pem_bundle", "pem_json", or "insecure_tls" is set

* If "insecure_tls" is set to true, the connection will not perform verification of the server certificate; this also sets "tls" to true

* If only "issuing_ca" is set in "pem_json", or the only certificate in "pem_bundle" is a CA certificate, the given CA certificate will be used for server certificate verification; otherwise the system CA certificates will be used

* If "certificate" and "private_key" are set in "pem_bundle" or "pem_json", client auth will be turned on for the connection

"pem_bundle" should be a PEM-concatenated bundle of a private key + client certificate, an issuing CA certificate, or both. "pem_json" should contain the same information; for convenience, the JSON format is the same as that output by the issue command from the PKI backend.

When configuring the connection information, the backend will verify its
validity.
`