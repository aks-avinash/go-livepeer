package core

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/livepeer/go-livepeer/common"
	"github.com/livepeer/go-livepeer/monitor"
	"github.com/livepeer/lpms/ffmpeg"
	"github.com/livepeer/lpms/transcoder"

	"github.com/golang/glog"
)

type Transcoder interface {
	Transcode(fname string, profiles []ffmpeg.VideoProfile) ([][]byte, error)
}

type LocalTranscoder struct {
	workDir string
}

func (lt *LocalTranscoder) Transcode(fname string, profiles []ffmpeg.VideoProfile) ([][]byte, error) {
	tr := transcoder.NewFFMpegSegmentTranscoder(profiles, lt.workDir)
	mid, seqNo, parseErr := parseURI(fname)
	start := time.Now()
	if monitor.Enabled && parseErr == nil {
		monitor.LogSegmentTranscodeStarting(seqNo, mid)
	}
	data, err := tr.Transcode(fname)
	if monitor.Enabled && parseErr == nil {
		monitor.LogSegmentTranscodeEnded(seqNo, mid, time.Since(start), common.ProfilesNames(profiles))
	}
	return data, err
}

func NewLocalTranscoder(workDir string) Transcoder {
	return &LocalTranscoder{workDir: workDir}
}

type NvidiaTranscoder struct {
	workDir string
	devices []string

	// The following fields need to be protected by the mutex `mu`
	mu     *sync.Mutex
	curDev int
}

func (nv *NvidiaTranscoder) getDevice() string {
	nv.mu.Lock()
	defer nv.mu.Unlock()
	nv.curDev = (nv.curDev + 1) % len(nv.devices)
	return nv.devices[nv.curDev]
}

func (nv *NvidiaTranscoder) Transcode(fname string, profiles []ffmpeg.VideoProfile) ([][]byte, error) {
	// Set up in / out config
	in := &ffmpeg.TranscodeOptionsIn{
		Fname:  fname,
		Accel:  ffmpeg.Nvidia,
		Device: nv.getDevice(),
	}
	opts := make([]ffmpeg.TranscodeOptions, len(profiles), len(profiles))
	for i := range profiles {
		o := ffmpeg.TranscodeOptions{
			Oname:   fmt.Sprintf("%s/out_%s_%d.%s", nv.workDir, RandomManifestID(), i, filepath.Base(fname)),
			Profile: profiles[i],
			Accel:   ffmpeg.Nvidia,
		}
		opts[i] = o
	}

	// Do the Transcoding
	if err := ffmpeg.Transcode2(in, opts); err != nil {
		return [][]byte{}, err
	}

	// Convert results into in-memory bytes following the expected API
	out := make([][]byte, len(opts), len(opts))
	for i := range opts {
		oname := opts[i].Oname
		o, err := ioutil.ReadFile(oname)
		if err != nil {
			glog.Error("Cannot read transcoded output for ", oname)
			return [][]byte{}, err
		}
		out[i] = o
		os.Remove(oname)
	}
	return out, nil
}

func NewNvidiaTranscoder(inp string, workDir string) Transcoder {
	devices := strings.Split(inp, ",")
	return &NvidiaTranscoder{devices: devices, workDir: workDir, mu: &sync.Mutex{}}
}

func parseURI(uri string) (string, uint64, error) {
	var mid string
	var seqNo uint64
	parts := strings.Split(uri, "/")
	if len(parts) < 3 {
		return mid, seqNo, fmt.Errorf("BadURI")
	}
	mid = parts[len(parts)-2]
	parts = strings.Split(parts[len(parts)-1], ".")
	seqNo, err := strconv.ParseUint(parts[0], 10, 64)
	return mid, seqNo, err
}
