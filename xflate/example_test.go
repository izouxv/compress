// Copyright 2016, Joe Tsai. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE.md file.

package xflate_test

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"io/ioutil"
	"log"
	"math"
	mathrand "math/rand"
	"testing"

	"github.com/dsnet/compress/internal/testutil"
	"github.com/dsnet/compress/xflate"
)

func init() { log.SetFlags(log.Lshortfile) }

// Zip archives allow for efficient random access between files, however,
// they do not easily allow for efficient random access within a given file,
// especially if compressed. In this example, we use XFLATE to compress each
// file. This is particularly useful for seeking within a relatively large
// file in a Zip archive.
func Example_zipFile() {
	// Test files of non-trivial sizes.
	files := map[string][]byte{
		"twain.txt":   testutil.MustLoadFile("../testdata/twain.txt"),
		"digits.txt":  testutil.MustLoadFile("../testdata/digits.txt"),
		"huffman.txt": testutil.MustLoadFile("../testdata/huffman.txt"),
	}

	// Write the Zip archive.
	buffer := new(bytes.Buffer)
	zw := zip.NewWriter(buffer)
	zw.RegisterCompressor(zip.Deflate, func(wr io.Writer) (io.WriteCloser, error) {
		// Instead of the default DEFLATE compressor, register one that uses
		// XFLATE instead. We choose a relative small chunk size of 64KiB for
		// better random access properties, at the expense of compression ratio.
		return xflate.NewWriter(wr, &xflate.WriterConfig{
			Level:     xflate.BestSpeed,
			ChunkSize: 1 << 16,
		})
	})
	for _, name := range []string{"twain.txt", "digits.txt", "huffman.txt"} {
		body := files[name]
		f, err := zw.Create(name)
		if err != nil {
			log.Fatal(err)
		}
		if _, err = f.Write(body); err != nil {
			log.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}

	// Read the Zip archive.
	rd := bytes.NewReader(buffer.Bytes())
	zr, err := zip.NewReader(rd, rd.Size())
	if err != nil {
		log.Fatal(err)
	}
	for _, f := range zr.File {
		// Verify that the new compression format is backwards compatible with
		// a standard DEFLATE decompressor.
		rc, err := f.Open()
		if err != nil {
			log.Fatal(err)
		}
		buf, err := ioutil.ReadAll(rc)
		if err != nil {
			log.Fatal(err)
		}
		if err := rc.Close(); err != nil {
			log.Fatal(err)
		}
		if !bytes.Equal(buf, files[f.Name]) {
			log.Fatal("file content does not match")
		}
	}
	for _, f := range zr.File {
		// In order for XFLATE to provide random access, it needs to be provided
		// an io.ReadSeeker in order to operate. Thus, get low-level access to
		// the compressed file data in archive.
		off, err := f.DataOffset()
		if err != nil {
			log.Fatal(err)
		}
		rds := io.NewSectionReader(rd, off, int64(f.CompressedSize64))

		// Since we know that the writer used the XFLATE format, we can open
		// the compressed file as an xflate.Reader. If the file was compressed
		// with regular DEFLATE, then this will return an error.
		xr, err := xflate.NewReader(rds, nil)
		if err != nil {
			log.Fatal(err)
		}

		// Read from the middle of the file.
		buf := make([]byte, 80)
		pos := int64(f.UncompressedSize64 / 2)
		if _, err := xr.Seek(pos, io.SeekStart); err != nil {
			log.Fatal(err)
		}
		if _, err := io.ReadFull(xr, buf); err != nil {
			log.Fatal(err)
		}

		// Close the Reader.
		if err := xr.Close(); err != nil {
			log.Fatal(err)
		}

		got := string(buf)
		want := string(files[f.Name][pos : pos+80])
		fmt.Printf("File: %s\n\tgot:  %q\n\twant: %q\n\n", f.Name, got, want)
	}

	// Output:
	// File: twain.txt
	// 	got:  "ver, white with foam, the driving spray of spume-flakes, the dim\noutlines of the"
	// 	want: "ver, white with foam, the driving spray of spume-flakes, the dim\noutlines of the"
	//
	// File: digits.txt
	// 	got:  "63955008002334767618706808652687872278317742021406898070341050620023527363226729"
	// 	want: "63955008002334767618706808652687872278317742021406898070341050620023527363226729"
	//
	// File: huffman.txt
	// 	got:  "E+uXeMsjFSXvhrGmRZCF7ErSVMWoWEzqMdW8uRyjCRxkQxOrWrQgkSdHshJyTbsBajQUoNfPY1zuLRvy"
	// 	want: "E+uXeMsjFSXvhrGmRZCF7ErSVMWoWEzqMdW8uRyjCRxkQxOrWrQgkSdHshJyTbsBajQUoNfPY1zuLRvy"
}

// The Gzip format (RFC 1952) is a framing format for DEFLATE (RFC 1951).
// For this reason, we can provide random access decompression to Gzip files
// that are compressed with XFLATE. The example below adds a lightweight
// header and footer to the XFLATE stream to make it compliant with the Gzip
// format. This has the advantage that these files remain readable by
// standard implementations of Gzip. Note that regular Gzip files are not
// seekable because they are not compressed in the XFLATE format.
func Example_gzipFile() {
	// Test file of non-trivial size.
	twain := testutil.MustLoadFile("../testdata/twain.txt")

	// The Gzip header without using any extra features is 10 bytes long.
	const header = "\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\xff"

	// Write the Gzip file.
	buffer := new(bytes.Buffer)
	{
		// Write Gzip header.
		buffer.WriteString(header)

		// Instead of using flate.Writer, we use xflate.Writer instead.
		// We choose a relative small chunk size of 64KiB for better
		// random access properties, at the expense of compression ratio.
		xw, err := xflate.NewWriter(buffer, &xflate.WriterConfig{
			Level:     xflate.BestSpeed,
			ChunkSize: 1 << 16,
		})
		if err != nil {
			log.Fatal(err)
		}

		// Write the test data.
		crc := crc32.NewIEEE()
		mw := io.MultiWriter(xw, crc) // Write to both compressor and hasher
		if _, err := io.Copy(mw, bytes.NewReader(twain)); err != nil {
			log.Fatal(err)
		}
		if err := xw.Close(); err != nil {
			log.Fatal(err)
		}

		// Write Gzip footer.
		binary.Write(buffer, binary.LittleEndian, uint32(crc.Sum32()))
		binary.Write(buffer, binary.LittleEndian, uint32(len(twain)))
	}

	// Verify that Gzip file is RFC 1952 compliant.
	{
		gz, err := gzip.NewReader(bytes.NewReader(buffer.Bytes()))
		if err != nil {
			log.Fatal(err)
		}
		buf, err := ioutil.ReadAll(gz)
		if err != nil {
			log.Fatal(err)
		}
		if !bytes.Equal(buf, twain) {
			log.Fatal("gzip content does not match")
		}
	}

	// Read the Gzip file.
	{
		// Parse and discard the Gzip wrapper.
		// This does not work for back-to-back Gzip files.
		var hdr [10]byte
		rd := bytes.NewReader(buffer.Bytes())
		if _, err := rd.ReadAt(hdr[:], 0); err != nil {
			log.Fatal(err)
		}
		if string(hdr[:3]) != header[:3] || rd.Size() < 18 {
			log.Fatal("not a gzip file")
		}
		if hdr[3]&0xfe > 0 {
			log.Fatal("no support for extra gzip features")
		}
		rds := io.NewSectionReader(rd, 10, rd.Size()-18) // Strip Gzip header/footer

		// Since we know that the writer used the XFLATE format, we can open
		// the compressed file as an xflate.Reader. If the file was compressed
		// with regular DEFLATE, then this will return an error.
		xr, err := xflate.NewReader(rds, nil)
		if err != nil {
			log.Fatal(err)
		}

		// Read from the middle of the stream.
		buf := make([]byte, 80)
		pos := int64(len(twain) / 2)
		if _, err := xr.Seek(pos, io.SeekStart); err != nil {
			log.Fatal(err)
		}
		if _, err := io.ReadFull(xr, buf); err != nil {
			log.Fatal(err)
		}

		// Close the Reader.
		if err := xr.Close(); err != nil {
			log.Fatal(err)
		}

		got := string(buf)
		want := string(twain[pos : pos+80])
		fmt.Printf("got:  %q\nwant: %q\n", got, want)
	}

	// Output:
	// got:  "ver, white with foam, the driving spray of spume-flakes, the dim\noutlines of the"
	// want: "ver, white with foam, the driving spray of spume-flakes, the dim\noutlines of the"
}

type seekItem struct {
	off     int64
	seek    int
	n       int
	want    string
	wantpos int64
	readerr error
	seekerr string
}

var testSeekPlaintext = []byte("0123456789")

var testSeekItems = []seekItem{

	{seek: io.SeekStart, off: 0, n: 20, want: "0123456789"},
	{seek: io.SeekStart, off: 0, n: 5, want: "01234"},

	{seek: io.SeekCurrent, off: 1, wantpos: 6, n: 1, want: "6"},
	{seek: io.SeekCurrent, off: -3, wantpos: 4, n: 3, want: "456"},

	{seek: io.SeekStart, off: 1, n: 1, want: "1"},
	{seek: io.SeekCurrent, off: 1, wantpos: 3, n: 2, want: "34"},

	{seek: io.SeekStart, off: -1, seekerr: "xflate: invalid argument: negative position: -1"},
	// {seek: io.SeekStart, off: -1, seekerr: "bytes.Reader.Seek: negative position"},
	{seek: io.SeekStart, off: 1 << 33, wantpos: 1 << 33, readerr: io.EOF},
	{seek: io.SeekCurrent, off: 1, wantpos: 1<<33 + 1, readerr: io.EOF},

	{seek: io.SeekStart, n: 5, want: "01234"},
	{seek: io.SeekCurrent, n: 5, want: "56789"},
	{seek: io.SeekEnd, off: -1, n: 1, wantpos: 9, want: "9"},

	{seek: io.SeekStart, n: 0},
}

func testSeek(t *testing.T, chunkSize int64, tests []seekItem, r io.ReadSeeker) {
	for i, tt := range tests {
		pos, err := r.Seek(tt.off, tt.seek)
		if err == nil && tt.seekerr != "" {
			t.Errorf("%d. want seek error %q", i, tt.seekerr)
			continue
		}
		if err != nil && err.Error() != tt.seekerr {
			t.Errorf("%d. seek error = %q; want %q", i, err.Error(), tt.seekerr)
			continue
		}
		if tt.wantpos != 0 && tt.wantpos != pos {
			t.Errorf("%d. pos = %d, want %d", i, pos, tt.wantpos)
		}
		buf := make([]byte, tt.n)
		n, err := r.Read(buf)
		if err != tt.readerr {
			t.Errorf("%d. read = %v; want %v", i, err, tt.readerr)
			continue
		}
		got := string(buf[:n])
		if got != tt.want {
			t.Errorf("%d. got %q; want %q, chunkSize: %v", i, got, tt.want, chunkSize)
		}
	}
}

func TestSeekDeflate(t *testing.T) {
	v_input := testSeekPlaintext

	pow := float64(mathrand.Intn(8) + 1)
	chunkSize := int64(math.Pow(2, pow))

	var wb, rb bytes.Buffer
	xw, err := xflate.NewWriter(&wb, &xflate.WriterConfig{
		ChunkSize: chunkSize,
	})
	if err != nil {
		t.Errorf("unexpected error: NewWriter() = %v", err)
	}
	cnt2, err := xw.Write(v_input)
	cnt := int64(cnt2)
	// cnt, err := io.Copy(xw, bytes.NewReader(v_input))
	if err != nil {
		t.Errorf("unexpected error: Write() = %v", err)
	}
	if cnt != int64(len(v_input)) {
		t.Errorf("write count mismatch: got %d, want %d", cnt, len(v_input))
	}
	if err := xw.Close(); err != nil {
		t.Errorf("unexpected error: Close() = %v", err)
	}

	xr, err := xflate.NewReader(bytes.NewReader(wb.Bytes()), &xflate.ReaderConfig{})
	if err != nil {
		t.Errorf("unexpected error: NewReader() = %v", err)
	}

	testSeek(t, chunkSize, testSeekItems, xr)

	cnt, err = io.Copy(&rb, xr)
	if err != nil {
		t.Errorf("unexpected error: Read() = %v", err)
	}
	if cnt != int64(len(v_input)) {
		t.Errorf("read count mismatch: got %d, want %d", cnt, len(v_input))
	}

	if err := xr.Close(); err != nil {
		t.Errorf("unexpected error: Close() = %v", err)
	}

}
