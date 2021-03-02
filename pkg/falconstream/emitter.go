package falconstream

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/k0kubun/pp"
	"github.com/fivetran/gofalcon"
	"github.com/pkg/errors"
)

// EmitterArguments is arguments for all emitter.
type EmitterArguments struct {
	Type string // fs or console

	// fs
	FsDir            string
	FsFileNamePrefix string

	// s3
	AwsRegion   string
	AwsS3Bucket string
	AwsS3Prefix string
}

type falconEvent struct {
	MetaData *gofalcon.StreamEventMetaData `json:"metadata"`
	Event    map[string]interface{}        `json:"event"`
}

func newEmitter(args EmitterArguments) emitter {
	switch args.Type {
	case "fs":
		return &fsEmitter{}
	case "console":
		return &consoleEmitter{}
	case "s3":
		return &s3Emitter{args: args}
	default:
		return nil
	}
}

type emitter interface {
	setup() error
	teardown() error
	emit(*falconEvent) error
}

type fsEmitter struct {
	fileName string
	fs       *os.File
}

func (x *fsEmitter) setup() error {
	x.fileName = "falcon.log"

	fs, err := os.Create(x.fileName)
	if err != nil {
		return errors.Wrapf(err, "Fail to create log file: %s", x.fileName)
	}

	x.fs = fs
	return nil
}

func (x *fsEmitter) teardown() error {
	if err := x.fs.Close(); err != nil {
		return errors.Wrapf(err, "Fail to close log file: %s", x.fileName)
	}
	return nil
}

func (x *fsEmitter) emit(ev *falconEvent) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return errors.Wrapf(err, "Fail to marshal event data: %v", ev)
	}

	if _, err := x.fs.Write(raw); err != nil {
		return errors.Wrapf(err, "Fail to write log data")
	}
	if _, err := x.fs.Write([]byte("\n")); err != nil {
		return errors.Wrapf(err, "Fail to write new line code")
	}

	return nil
}

type consoleEmitter struct{}

func (x *consoleEmitter) setup() error    { return nil }
func (x *consoleEmitter) teardown() error { return nil }
func (x *consoleEmitter) emit(ev *falconEvent) error {
	if _, err := pp.Println(*ev); err != nil {
		return errors.Wrap(err, "Fail to output event by pp")
	}
	return nil
}

type s3Emitter struct {
	args     EmitterArguments
	s3client *s3.S3
}

func (x *s3Emitter) setup() error {
	if x.args.AwsRegion == "" || x.args.AwsS3Bucket == "" {
		return fmt.Errorf("aws-region and aws-s3-bucket are required for S3 emitter")
	}

	ssn := session.Must(session.NewSession(&aws.Config{
		Region: aws.String(x.args.AwsRegion),
	}))
	x.s3client = s3.New(ssn)

	return nil
}
func (x *s3Emitter) teardown() error { return nil }
func (x *s3Emitter) emit(ev *falconEvent) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return errors.Wrapf(err, "Fail to marshal Falcon Event: %v", *ev)
	}

	t := time.Unix(ev.MetaData.EventCreationTime/1000, 0)
	h := sha256.New()
	if _, err := h.Write(raw); err != nil {
		return errors.Wrap(err, "Fail to write buffer for sha256 hash")
	}
	fname := t.Format("20060102_150405_") + fmt.Sprintf("%x.json.gz", h.Sum(nil))
	s3Key := x.args.AwsS3Prefix + t.Format("2006/01/02") + "/" + fname
	s3Path := "s3://" + x.args.AwsS3Bucket + "/" + s3Key

	Logger.WithField("s3path", s3Path).Trace("Uploading s3 object")

	_, err = x.s3client.HeadObject(&s3.HeadObjectInput{
		Bucket: &x.args.AwsS3Bucket,
		Key:    &s3Key,
	})

	exists := true
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case s3.ErrCodeNoSuchKey:
				exists = false
			case "NotFound":
				exists = false
			default:
				return errors.Wrapf(err, "HeadObject error: %s", aerr.Error())
			}
		} else {
			return errors.Wrap(err, "HeadObject error")
		}
	}

	if !exists {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(raw); err != nil {
			return errors.Wrap(err, "Fail to write gzip stream for event")
		}
		zw.Close()

		_, err = x.s3client.PutObject(&s3.PutObjectInput{
			Body:   bytes.NewReader(buf.Bytes()),
			Bucket: &x.args.AwsS3Bucket,
			Key:    &s3Key,
		})
		if err != nil {
			return errors.Wrapf(err, "Fail to put log object: %s", s3Path)
		}
		Logger.WithField("s3path", s3Path).Trace("Object uploaded")
	} else {
		Logger.WithField("s3path", s3Path).Trace("Object already exists")
	}

	return nil
}
