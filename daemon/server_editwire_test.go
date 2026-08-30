package main

import (
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"

	"codeterminal/protocol"
)

// The wire half of the syntax gate and the block parser.
//
// These exist because the fields they cover are the kind that a client half
// silently stops reading and no Go test notices: everything still compiles,
// every existing assertion still passes, and the only symptom is a person not
// being told something.

// TestParseAndLogEditBlocksReturnsRejections covers the case that motivated the
// field: a response whose edits are ALL malformed. It used to return nil and
// send the client nothing, so a user watched a reply that proposed edits do
// absolutely nothing and had no way to find out why without the daemon log.
func TestParseAndLogEditBlocksReturnsRejections(t *testing.T) {
	s := &Server{logger: log.New(os.Stderr, "", 0)}

	t.Run("all blocks malformed", func(t *testing.T) {
		blocks, rejections := s.parseAndLogEditBlocks(
			"here you go\n\n" +
				"path: foo.go\n<<<<<<< SEARCH\nold\n=======\nnew\n")
		if len(blocks) != 0 {
			t.Fatalf("got %d usable blocks, want 0", len(blocks))
		}
		if len(rejections) == 0 {
			t.Fatal("a response whose only edit block is unterminated returned NO rejections. " +
				"The client is then told nothing at all -- no proposals, no reason -- which " +
				"is the exact case this field was added for")
		}
		if rejections[0].Line == 0 || rejections[0].Reason == "" {
			t.Errorf("rejection = %+v, want a line number and a reason a user can act on", rejections[0])
		}
	})

	t.Run("good and bad together", func(t *testing.T) {
		blocks, rejections := s.parseAndLogEditBlocks(
			"path: foo.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n\n" +
				"path: bar.go\n<<<<<<< SEARCH\nunterminated\n")
		if len(blocks) != 1 {
			t.Fatalf("got %d usable blocks, want the one good block to survive", len(blocks))
		}
		if len(rejections) != 1 {
			t.Fatalf("got %d rejections, want the bad block reported alongside the good one", len(rejections))
		}
	})

	t.Run("clean response carries no rejections field at all", func(t *testing.T) {
		_, rejections := s.parseAndLogEditBlocks(
			"path: foo.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n")
		if rejections != nil {
			t.Errorf("rejections = %+v, want nil so omitempty drops the field entirely. "+
				"An empty [] on every clean response is noise on the wire and reads to a "+
				"client as a thing that happened", rejections)
		}
	})
}

// TestEditWireFieldsAreAdditive is the compatibility half, and the one a
// hand-written client breaks. Both new payloads must decode into a struct that
// has never heard of them, and both must vanish from the JSON when unset.
func TestEditWireFieldsAreAdditive(t *testing.T) {
	t.Run("ApplyEditResponse omits the notes when unset", func(t *testing.T) {
		b, err := json.Marshal(protocol.ApplyEditResponse{
			ProtocolVersion: protocol.ProtocolVersion, Applied: true, BackupDir: "/tmp/x",
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "syntax_note") || strings.Contains(string(b), "match_note") {
			t.Errorf("JSON = %s, want the notes absent when empty", b)
		}
	})

	t.Run("ApplyEditResponse carries the notes when set", func(t *testing.T) {
		b, _ := json.Marshal(protocol.ApplyEditResponse{
			ProtocolVersion: protocol.ProtocolVersion, Applied: true,
			SyntaxNote: "go/parser OK", MatchNote: "matched after normalising line endings",
		})
		var back protocol.ApplyEditResponse
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		if back.SyntaxNote != "go/parser OK" || back.MatchNote == "" {
			t.Errorf("round trip lost a note: %+v", back)
		}
		// An older client's struct, which has never heard of these fields.
		var old struct {
			Applied   bool   `json:"applied"`
			BackupDir string `json:"backup_dir"`
		}
		if err := json.Unmarshal(b, &old); err != nil {
			t.Fatalf("a client that predates these fields can no longer decode the response: %v", err)
		}
		if !old.Applied {
			t.Error("the older shape lost the field it does understand")
		}
	})

	t.Run("TokenResponse rejections round-trip and stay optional", func(t *testing.T) {
		b, _ := json.Marshal(protocol.TokenResponse{
			ProtocolVersion: protocol.ProtocolVersion, Done: true,
			EditRejections: []protocol.EditRejectionWire{{Line: 47, Reason: "no closing REPLACE marker"}},
		})
		var back protocol.TokenResponse
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		if len(back.EditRejections) != 1 || back.EditRejections[0].Line != 47 {
			t.Errorf("round trip lost the rejection: %+v", back.EditRejections)
		}

		clean, _ := json.Marshal(protocol.TokenResponse{
			ProtocolVersion: protocol.ProtocolVersion, Done: true,
		})
		if strings.Contains(string(clean), "edit_rejections") {
			t.Errorf("JSON = %s, want the field absent when there are no rejections", clean)
		}
	})
}
