// Package protocol is the contract between the moin agent and the control
// plane. The agent dials out: it registers once over HTTP, then holds one
// WebSocket session. The agent and the control plane both import this package,
// so a change here is a change on both sides.
package protocol

import (
	"encoding/json"
	"errors"
)

// Version is the protocol version the agent reports in Hello. Bump it when a
// message changes in a way an older peer cannot read. Version 2 brought
// backups as one history to go back in, without snapshots.
const Version = 2

// Needs is the protocol version an agent must speak for a request: the
// control plane answers "update the agent" to an older one instead of
// sending it.
func Needs(messageType MessageType) int {
	switch messageType {
	case TypeBackupList, TypeBackupRestore, TypeBackupDownload, TypeBackupRestores:
		return 2
	}
	return 1
}

const (
	// RegisterPath trades a one-time install token, sent as
	// "Authorization: Bearer <install token>", for an agent token.
	RegisterPath = "/agent/register"
	// SessionPath upgrades to the WebSocket session, authorized with
	// "Authorization: Bearer <agent token>".
	SessionPath = "/agent/session"
)

// MessageType decides how Message.Data decodes.
type MessageType string

const (
	// TypeHello is the first message the agent sends on every session.
	TypeHello MessageType = "hello"

	// TypeDeployStart asks the agent to deploy an app (DeployStart).
	TypeDeployStart MessageType = "deploy.start"
	// TypeDeployLog is one line of deploy output from the agent (DeployLog).
	TypeDeployLog MessageType = "deploy.log"
	// TypeDeployDone ends a deploy (DeployDone).
	TypeDeployDone MessageType = "deploy.done"

	// TypeLogs asks for the last lines of an app's output (LogsRequest).
	// The agent answers with TypeLogsResult or TypeError.
	TypeLogs       MessageType = "logs"
	TypeLogsResult MessageType = "logs.result"

	// TypeAppRemove stops an app and removes its containers and images
	// (AppRemove). Its data directory stays. The agent answers with TypeOK
	// or TypeError.
	TypeAppRemove MessageType = "app.remove"
	// TypeAppStop stops the containers of an app and keeps them, so they
	// stay stopped after a reboot (AppStop). A deploy or a restart starts
	// them again. The agent answers with TypeOK or TypeError.
	TypeAppStop MessageType = "app.stop"

	// TypeDBTables lists the tables of an app's database, a SQLite file or
	// a Postgres service (DBRequest). The agent answers with
	// TypeDBTablesResult or TypeError.
	TypeDBTables       MessageType = "db.tables"
	TypeDBTablesResult MessageType = "db.tables.result"
	// TypeDBQuery runs SQL against an app's database, SQLite or Postgres
	// (DBQuery). The agent answers with TypeDBQueryResult or TypeError.
	TypeDBQuery       MessageType = "db.query"
	TypeDBQueryResult MessageType = "db.query.result"

	// TypeBackupConfig tells the agent where backups go (BackupConfig). The
	// control plane sends it after hello and whenever it changes. The agent
	// answers with TypeOK.
	TypeBackupConfig MessageType = "backup.config"
	// TypeBackupList says how the backups of an app stand (BackupRequest).
	// The agent answers with TypeBackupsResult.
	TypeBackupList    MessageType = "backup.list"
	TypeBackupsResult MessageType = "backups.result"
	// TypeBackupRestore puts a database back to a moment, or to the copy
	// before an earlier restore (BackupRestore). The agent answers with
	// TypeOK or TypeError.
	TypeBackupRestore MessageType = "backup.restore"
	// TypeBackupRestores asks how the restores of an app stand
	// (BackupRequest), without looking into the bucket: also when the app
	// has no bucket or is gone. The agent answers with TypeBackupsResult,
	// whose Restores is the answer and Databases empty.
	TypeBackupRestores MessageType = "backup.restores"
	// TypeBackupDownload makes a full copy of one database in the bucket's
	// downloads/ folder (BackupRequest). The agent answers with
	// TypeBackupFile or TypeError.
	TypeBackupDownload MessageType = "backup.download"
	TypeBackupFile     MessageType = "backup.file"
	// TypeBackupStatus is how the backups of an app stand (Backups), sent by
	// the agent after a daily check, or when a backup breaks or works again.
	TypeBackupStatus MessageType = "backup.status"

	// TypeServerConfig sets how the server is reached and locked down
	// (ServerConfig). The control plane sends it after hello and whenever
	// it changes. The agent answers with TypeServerConfigResult.
	TypeServerConfig       MessageType = "server.config"
	TypeServerConfigResult MessageType = "server.config.result"

	// TypeAgentUpdate has the agent replace its binary with the one the
	// control plane serves under DownloadPath, check it against SHA256 and
	// restart (AgentUpdate). The agent answers with TypeOK before it
	// restarts, or with TypeError and changes nothing.
	TypeAgentUpdate MessageType = "agent.update"

	// TypeAgentURL tells the agent the control plane's address when it
	// dialed an old one (AgentURL). The agent checks that the address
	// answers, keeps it and dials it from then on. It has no request ID and
	// no answer: an agent that does not know it ignores it.
	TypeAgentURL MessageType = "agent.url"

	// TypeMetrics is a sample of the server and its apps (Metrics), sent by
	// the agent every MetricsEvery without a request. A control plane that
	// does not know it drops it; an agent that does not send it shows no
	// metrics.
	TypeMetrics MessageType = "metrics"

	// TypeExecStart runs one command in a container of an app (ExecStart).
	// The agent answers with TypeExecOutput messages as the command writes,
	// then one TypeExecDone, all with the request's ID. An agent that does
	// not know it answers TypeError.
	TypeExecStart  MessageType = "exec.start"
	TypeExecOutput MessageType = "exec.output"
	TypeExecDone   MessageType = "exec.done"

	// TypeOK answers a request that has no result.
	TypeOK MessageType = "ok"
	// TypeError answers a request that failed (Error).
	TypeError MessageType = "error"
)

// Message is the WebSocket envelope. A request carries a RequestID, and its
// answer carries the same one.
type Message struct {
	Type      MessageType     `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// NewMessage wraps data in an envelope of the given type.
func NewMessage(messageType MessageType, data any) (Message, error) {
	message := Message{Type: messageType}
	if data == nil {
		return message, nil
	}
	body, err := json.Marshal(data)
	if err != nil {
		return Message{}, err
	}
	message.Data = body
	return message, nil
}

// Decode reads Data into target.
func (m Message) Decode(target any) error {
	if len(m.Data) == 0 {
		return errors.New("message has no data")
	}
	return json.Unmarshal(m.Data, target)
}

// Error is the answer to a request that failed.
type Error struct {
	Message string `json:"message"`
}
