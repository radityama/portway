// Package protocol implements Portway v1 framing and capability negotiation.
// It does not own network connections. Callers serialize writes, set deadlines,
// and close their connection to cancel blocked I/O. Treat codec errors as terminal.
package protocol
