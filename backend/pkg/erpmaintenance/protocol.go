// Package erpmaintenance implements the closed, same-UID local maintenance protocol.
package erpmaintenance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const Protocol = "yanfa-erp-maintenance/1"
const MaxFrame = 2048

var ErrProtocol = errors.New("MAINTENANCE_PROTOCOL_INVALID")
var ErrUnavailable = errors.New("MAINTENANCE_BACKEND_UNAVAILABLE")
var ErrUnknown = errors.New("MAINTENANCE_RESULT_UNKNOWN")

type Request struct {
	Protocol  string `json:"protocol"`
	SourceID  string `json:"source_id"`
	RequestID string `json:"request_id"`
	Operation string `json:"operation"`
}
type Response struct {
	Protocol     string `json:"protocol"`
	RequestID    string `json:"request_id"`
	OperationRef string `json:"operation_ref"`
	Code         string `json:"code"`
}

func canonical(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

// Decode requires exact keys, no duplicate/case aliases and one bounded JSONL frame.
func Decode(reader io.Reader, target interface{}) error {
	input := bufio.NewReaderSize(reader, MaxFrame+1)
	line, err := input.ReadSlice('\n')
	if err != nil || len(line) > MaxFrame {
		return ErrProtocol
	}
	if _, err := input.ReadByte(); err != io.EOF {
		return ErrProtocol
	}
	var allowed map[string]bool
	switch target.(type) {
	case *Request:
		allowed = map[string]bool{"protocol": true, "source_id": true, "request_id": true, "operation": true}
	case *Response:
		allowed = map[string]bool{"protocol": true, "request_id": true, "operation_ref": true, "code": true}
	default:
		return ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(line))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return ErrProtocol
	}
	seen := make(map[string]bool, len(allowed))
	for d.More() {
		t, err := d.Token()
		key, ok := t.(string)
		if err != nil || !ok || !allowed[key] || seen[key] {
			return ErrProtocol
		}
		seen[key] = true
		var value string
		if d.Decode(&value) != nil {
			return ErrProtocol
		}
	}
	if len(seen) != len(allowed) {
		return ErrProtocol
	}
	if _, err := d.Token(); err != nil {
		return ErrProtocol
	}
	if d.Decode(new(interface{})) != io.EOF || json.Unmarshal(line, target) != nil {
		return ErrProtocol
	}
	switch v := target.(type) {
	case *Request:
		if v.Protocol != Protocol || !canonical(v.SourceID) || !canonical(v.RequestID) || v.Operation != "incremental" {
			return ErrProtocol
		}
	case *Response:
		if v.Protocol != Protocol || !canonical(v.RequestID) || v.OperationRef != "" && !canonical(v.OperationRef) {
			return ErrProtocol
		}
		switch v.Code {
		case "ACCEPTED", "SOURCE_BUSY", "DATA_SYNC_IN_PROGRESS", "DATA_SYNC_RECOVERING", "SYNC_NOT_READY", "SYNC_STATE_UNAVAILABLE", "REQUEST_CONFLICT", "INVALID_REQUEST":
		default:
			return ErrProtocol
		}
		if v.Code == "ACCEPTED" && v.OperationRef == "" {
			return ErrProtocol
		}
	}
	return nil
}
func Encode(writer io.Writer, value interface{}) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw)+1 > MaxFrame {
		return ErrProtocol
	}
	raw = append(raw, '\n')
	_, err = writer.Write(raw)
	return err
}

// CheckDirectory checks each ancestor without following links. The final gate
// directory must be 0700; no group/world-writable ancestor is accepted.
func CheckDirectory(path string, uid uint32) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrUnavailable
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { unix.Close(fd) }()
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return ErrUnavailable
		}
		unix.Close(fd)
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Mode&0022 != 0 || st.Uid != 0 && st.Uid != uid {
			return ErrUnavailable
		}
		if i == len(parts)-1 && (st.Uid != uid || st.Mode&07777 != 0700) {
			return ErrUnavailable
		}
	}
	return nil
}
func CheckSocket(path string, uid uint32) error {
	if CheckDirectory(filepath.Dir(path), uid) != nil {
		return ErrUnavailable
	}
	var st unix.Stat_t
	if unix.Lstat(path, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Uid != uid || st.Mode&07777 != 0600 {
		return ErrUnavailable
	}
	return nil
}
func SameUID(conn *net.UnixConn, uid uint32) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	valid := false
	if raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		valid = e == nil && cred.Uid == uid
	}) != nil {
		return false
	}
	return valid
}
func Call(directory string, uid uint32, request Request) (*Response, error) {
	path := filepath.Join(directory, "maintenance.sock")
	if uint32(os.Geteuid()) != uid || CheckSocket(path, uid) != nil {
		return nil, ErrUnavailable
	}
	conn, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer conn.Close()
	if !SameUID(conn.(*net.UnixConn), uid) {
		return nil, ErrUnavailable
	}
	conn.SetDeadline(time.Now().Add(45 * time.Second))
	// Once a write is attempted, any missing response is unknown. Never resend.
	if Encode(conn, request) != nil {
		return nil, ErrUnknown
	}
	if conn.(*net.UnixConn).CloseWrite() != nil {
		return nil, ErrUnknown
	}
	var response Response
	if Decode(conn, &response) != nil || response.RequestID != request.RequestID {
		return nil, ErrUnknown
	}
	return &response, nil
}
