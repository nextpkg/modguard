package sdk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandaloneConsumerUsesOnlyCopiedSDK(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOWORK", "off")
	dir := filepath.Join(root, "sdks")
	writeFixture(t, dir, "go.mod", "module example.org/sdk\n\ngo 1.26\n")
	if err := generate(dir, "single", "voice", "Transcribe", ""); err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		err := Check(dir, true, nil, nil)
		if want == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("wanted %q, got %v", want, err)
		}
	}
	// A generated, unimplemented method must fail the consumer acceptance test.
	check("not implemented")
	writeFixture(t, dir, "repository-only.txt", "present only in original checkout")
	writeFixture(t, dir, "voice/client.go", `package voice
import ("os"; "path/filepath"; "runtime"; "context")
type Client struct{}
func New(Config)*Client {return &Client{}}
func (*Client) Transcribe(context.Context,TranscribeInput)(TranscribeResult,error) {
 _,file,_,_:=runtime.Caller(0)
 _,err:=os.Stat(filepath.Join(filepath.Dir(file),"../repository-only.txt"))
 return TranscribeResult{},err
}`)
	if _, err := runGo(dir, "test", "./voice/testdata/consumer_test.go"); err != nil {
		t.Fatal(err)
	}
	check("repository-only.txt")
	writeFixture(t, dir, "voice/client.go", `package voice
import "context"
type Client struct{}
func New(Config)*Client{
 c:=&Client{}
 _,_ = c.Transcribe(context.Background(),TranscribeInput{})
 return c
}
func (*Client) String() string {return "voice"}
type Diagnostic int
func (Diagnostic) Error() string { return "diagnostic" }
func (*Client) Transcribe(context.Context,TranscribeInput)(TranscribeResult,error){return TranscribeResult{},nil}`)
	check("")
	writeFixture(t, dir, "voice/testdata/consumer_test.go", `package consumer_test
import "testing"
func TestSkipped(t *testing.T){t.Skip("no operation")}`)
	check("no passing tests")
	writeFixture(t, dir, "voice/testdata/consumer_test.go", `package consumer_test
import ("testing"; "example.org/sdk/voice")
func TestOnlyConstruct(t *testing.T){if voice.New(voice.Config{})==nil {t.Fatal("nil client")}}`)
	check("no SDK operation")
	writeFixture(t, dir, "voice/testdata/consumer_test.go", `package consumer_test
import ("testing"; "example.org/sdk/voice")
func TestDiagnosticsOnly(t *testing.T){
 c:=voice.New(voice.Config{})
 if c.String()!="voice" || voice.Diagnostic(1).Error()!="diagnostic"{ t.Fatal("formatting") }
 if false { _,_=c.Transcribe(t.Context(),voice.TranscribeInput{}) }
}`)
	check("no SDK operation")
	writeFixture(t, dir, "voice/internal/hidden/token.go", "package hidden\nconst Token=1\n")
	writeFixture(t, dir, "voice/testdata/consumer_test.go", `package consumer_test
import ("testing"; "example.org/sdk/voice/internal/hidden")
func TestPrivate(t *testing.T){t.Log(hidden.Token)}`)
	check("use of internal package")
	if err := os.Remove(filepath.Join(dir, "voice/testdata/consumer_test.go")); err != nil {
		t.Fatal(err)
	}
	check("consumer_test.go")
	if _, err := os.Stat(filepath.Join(dir, "voice/go.mod")); !os.IsNotExist(err) {
		t.Fatal("source directory acquired go.mod")
	}
}
