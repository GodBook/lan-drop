package qrcode

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/liyue201/goqr"
)

func TestEncodeBasicMatrix(t *testing.T) {
	qr, err := Encode("http://192.168.1.5:8087/?pin=1234", Medium)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	if qr.Size < 21 {
		t.Fatalf("matrix too small: %d (min QR version 1 is 21)", qr.Size)
	}
	if qr.Size%2 == 0 {
		t.Fatalf("QR size must be odd, got %d", qr.Size)
	}
	// Finder patterns: 7x7 solid dark border at three corners
	corners := [][2]int{{0, 0}, {qr.Size - 7, 0}, {0, qr.Size - 7}}
	for _, c := range corners {
		r0, c0 := c[0], c[1]
		for d := 0; d < 7; d++ {
			if !qr.Modules[r0][c0+d] {
				t.Errorf("finder border not dark at corner %v offset %d", c, d)
			}
			if !qr.Modules[r0+d][c0] {
				t.Errorf("finder border not dark at corner %v offset %d", c, d)
			}
		}
	}
}

func TestEncodeSelectsSupportedVersionsAtCapacityBoundaries(t *testing.T) {
	tests := []struct {
		length  int
		version int
	}{
		{length: 0, version: 1},
		{length: 14, version: 1},
		{length: 15, version: 2},
		{length: 26, version: 2},
		{length: 27, version: 3},
		{length: 42, version: 3},
		{length: 43, version: 4},
		{length: 62, version: 4},
		{length: 63, version: 5},
		{length: 84, version: 5},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d_bytes", tt.length), func(t *testing.T) {
			qr, err := Encode(strings.Repeat("x", tt.length), Medium)
			if err != nil {
				t.Fatalf("Encode failed: %v", err)
			}
			gotVersion := (qr.Size - 17) / 4
			if gotVersion != tt.version {
				t.Fatalf("got Version %d, want Version %d", gotVersion, tt.version)
			}
		})
	}
}

func TestEncodeRejectsPayloadBeyondVersionFive(t *testing.T) {
	qr, err := Encode(strings.Repeat("x", maxPayloadBytes+1), Medium)
	if qr != nil {
		t.Fatal("Encode returned a QR matrix for an oversized payload")
	}
	if !errors.Is(err, ErrDataTooLong) {
		t.Fatalf("got error %v, want ErrDataTooLong", err)
	}
}

func TestEncodeCapacityUsesUTF8ByteLength(t *testing.T) {
	maximum := strings.Repeat("界", 28) // 84 UTF-8 bytes.
	qr, err := Encode(maximum, Medium)
	if err != nil {
		t.Fatalf("Encode maximum UTF-8 payload failed: %v", err)
	}
	if gotVersion := (qr.Size - 17) / 4; gotVersion != 5 {
		t.Fatalf("got Version %d, want Version 5", gotVersion)
	}

	qr, err = Encode(maximum+"x", Medium)
	if qr != nil || !errors.Is(err, ErrDataTooLong) {
		t.Fatalf("85-byte UTF-8 payload returned qr=%v, err=%v", qr, err)
	}
}

func TestEncodeRejectsEveryNonMediumECL(t *testing.T) {
	levels := []ECL{Low, Quartile, High, ECL(-1), ECL(99)}
	for _, level := range levels {
		t.Run(fmt.Sprintf("level_%d", level), func(t *testing.T) {
			qr, err := Encode("hello", level)
			if qr != nil {
				t.Fatal("Encode returned a QR matrix for an unsupported ECL")
			}
			if !errors.Is(err, ErrUnsupportedECL) {
				t.Fatalf("got error %v, want ErrUnsupportedECL", err)
			}
		})
	}
}

func TestEncodeMatchesStandardVersionOneGoldenMatrix(t *testing.T) {
	qr, err := Encode("hello", Medium)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	// Generated independently with node-qrcode 1.5.4 in byte mode, Version 1,
	// Medium ECL, and mask pattern 0.
	want := strings.TrimSpace(`
#######..##...#######
#.....#.##....#.....#
#.###.#..#.##.#.###.#
#.###.#...##..#.###.#
#.###.#.##..#.#.###.#
#.....#.....#.#.....#
#######.#.#.#.#######
..........###........
#.#.#.#..#.#....#..#.
..#.##....#...#....##
.#.#..#.###.#...#####
##..#.........#....#.
.##.#.##..#.#.#.#....
........####.#.#..###
#######...##.###..###
#.....#...####.##....
#.###.#.#.##.###...##
#.###.#..#....##..##.
#.###.#.###.#...#.#.#
#.....#..#....#.#..#.
#######.###.#.##...##`)
	if got := matrixString(qr); got != want {
		t.Fatalf("matrix differs from the standards-compliant golden value:\n%s", got)
	}
}

func TestEncodeMatchesStandardVersionFiveGoldenDigest(t *testing.T) {
	qr, err := Encode(strings.Repeat("v", maxPayloadBytes), Medium)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	// Digest of the complete 37x37 matrix generated independently with
	// node-qrcode 1.5.4, Version 5, Medium ECL, and mask pattern 0. This covers
	// the highest supported version and its two interleaved ECC blocks.
	want := "286bc40f525bbbd5be87c66750b05200d1420cd1c0e606e65614750808b5f55c"
	got := fmt.Sprintf("%x", sha256.Sum256([]byte(matrixString(qr))))
	if got != want {
		t.Fatalf("Version 5 matrix digest = %s, want %s", got, want)
	}
}

func TestEncodedPayloadCanBeReadBackFromMatrix(t *testing.T) {
	inputs := []string{
		"hello",
		"http://192.168.1.5:8087/?pin=1234",
		strings.Repeat("v", maxPayloadBytes),
	}
	for _, input := range inputs {
		qr, err := Encode(input, Medium)
		if err != nil {
			t.Fatalf("Encode(%d bytes) failed: %v", len(input), err)
		}
		got, err := decodeBytePayload(qr)
		if err != nil {
			t.Fatalf("decodeBytePayload(%d bytes) failed: %v", len(input), err)
		}
		if got != input {
			t.Fatalf("decoded payload mismatch: got %q, want %q", got, input)
		}
	}
}

func TestEncodedMatrixCanBeDecodedByIndependentScanner(t *testing.T) {
	inputs := []string{
		"hello",
		"http://192.168.1.5:8087/?pin=1234",
		strings.Repeat("界", 28), // 84 UTF-8 bytes: Version 5 capacity boundary.
	}
	for _, input := range inputs {
		t.Run(fmt.Sprintf("%d_bytes", len(input)), func(t *testing.T) {
			qr, err := Encode(input, Medium)
			if err != nil {
				t.Fatalf("Encode failed: %v", err)
			}

			codes, err := goqr.Recognize(renderScannerImage(qr))
			if err != nil {
				t.Fatalf("independent QR decoder failed: %v", err)
			}
			if len(codes) != 1 {
				t.Fatalf("independent QR decoder found %d symbols, want 1", len(codes))
			}
			if got := string(codes[0].Payload); got != input {
				t.Fatalf("decoded payload mismatch: got %q, want %q", got, input)
			}
		})
	}
}

func TestToSmallTerminalString(t *testing.T) {
	qr, err := Encode("hello", Medium)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	out := qr.ToSmallTerminalString()
	if !strings.Contains(out, "\n") {
		t.Fatal("terminal rendering must be multi-line")
	}
	// The border guarantees at least one blank column; rendering must be non-empty
	if len(strings.TrimSpace(out)) == 0 {
		t.Fatal("terminal rendering is empty")
	}
}

func TestToSVGHasFourModuleQuietZone(t *testing.T) {
	qr, err := Encode("http://192.168.1.5:8087/?pin=1234", Medium)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	svg := qr.ToSVG()
	expectedViewBox := "viewBox=\"0 0 " + fmt.Sprint(qr.Size+8) + " " + fmt.Sprint(qr.Size+8) + "\""
	if !strings.Contains(svg, expectedViewBox) {
		t.Fatalf("SVG must include a four-module quiet zone: %s", expectedViewBox)
	}
	if !strings.Contains(svg, "<rect") || !strings.HasSuffix(svg, "</svg>") {
		t.Fatal("SVG output is incomplete")
	}
}

func matrixString(qr *QRCode) string {
	var b strings.Builder
	for y, row := range qr.Modules {
		if y > 0 {
			b.WriteByte('\n')
		}
		for _, dark := range row {
			if dark {
				b.WriteByte('#')
			} else {
				b.WriteByte('.')
			}
		}
	}
	return b.String()
}

func renderScannerImage(qr *QRCode) image.Image {
	const (
		quietZone  = 4
		moduleSize = 8
	)
	extent := (qr.Size + quietZone*2) * moduleSize
	img := image.NewGray(image.Rect(0, 0, extent, extent))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for row, modules := range qr.Modules {
		for column, dark := range modules {
			if !dark {
				continue
			}
			left := (column + quietZone) * moduleSize
			top := (row + quietZone) * moduleSize
			for y := top; y < top+moduleSize; y++ {
				for x := left; x < left+moduleSize; x++ {
					img.SetGray(x, y, color.Gray{Y: 0})
				}
			}
		}
	}
	return img
}

// decodeBytePayload independently walks the QR matrix using the standard data
// placement order. It keeps this package's scan-critical path covered without
// adding a decoder dependency to the production module.
func decodeBytePayload(qr *QRCode) (string, error) {
	if qr == nil || (qr.Size-17)%4 != 0 {
		return "", fmt.Errorf("invalid matrix size")
	}
	version := (qr.Size - 17) / 4
	if version < minSupportedVersion || version > maxSupportedVersion {
		return "", fmt.Errorf("unsupported version %d", version)
	}

	var bits []bool
	upwards := true
	for col := qr.Size - 1; col > 0; col -= 2 {
		if col == 6 {
			col--
		}
		for offset := 0; offset < qr.Size; offset++ {
			row := offset
			if upwards {
				row = qr.Size - 1 - offset
			}
			for currentCol := col; currentCol >= col-1; currentCol-- {
				if isFunctionModule(qr.Size, version, row, currentCol) {
					continue
				}
				bit := qr.Modules[row][currentCol]
				if (row+currentCol)%2 == 0 {
					bit = !bit
				}
				bits = append(bits, bit)
			}
		}
		upwards = !upwards
	}

	dataCodewords := [...]int{0, 16, 28, 44, 64, 86}
	eccCodewords := [...]int{0, 10, 16, 26, 36, 48}
	blockCounts := [...]int{0, 1, 1, 1, 2, 2}
	codewordCount := dataCodewords[version] + eccCodewords[version]
	if len(bits) < codewordCount*8 {
		return "", fmt.Errorf("matrix has %d data bits, need %d", len(bits), codewordCount*8)
	}

	codewords := make([]byte, codewordCount)
	for i := range codewords {
		for j := 0; j < 8; j++ {
			codewords[i] <<= 1
			if bits[i*8+j] {
				codewords[i] |= 1
			}
		}
	}

	blockCount := blockCounts[version]
	blockSize := dataCodewords[version] / blockCount
	blocks := make([][]byte, blockCount)
	for i := range blocks {
		blocks[i] = make([]byte, blockSize)
	}
	position := 0
	for i := 0; i < blockSize; i++ {
		for block := 0; block < blockCount; block++ {
			blocks[block][i] = codewords[position]
			position++
		}
	}
	data := make([]byte, 0, dataCodewords[version])
	for _, block := range blocks {
		data = append(data, block...)
	}

	bitPosition := 0
	mode, ok := readBits(data, &bitPosition, 4)
	if !ok || mode != 0b0100 {
		return "", fmt.Errorf("unexpected mode %04b", mode)
	}
	length, ok := readBits(data, &bitPosition, 8)
	if !ok || length > maxPayloadBytes {
		return "", fmt.Errorf("invalid byte count %d", length)
	}
	payload := make([]byte, length)
	for i := range payload {
		value, ok := readBits(data, &bitPosition, 8)
		if !ok {
			return "", fmt.Errorf("truncated payload")
		}
		payload[i] = byte(value)
	}
	return string(payload), nil
}

func isFunctionModule(size, version, row, col int) bool {
	if row <= 8 && col <= 8 {
		return true
	}
	if row <= 8 && col >= size-8 {
		return true
	}
	if row >= size-8 && col <= 8 {
		return true
	}
	if row == 6 || col == 6 {
		return true
	}
	if version >= 2 {
		alignmentStart := size - 9
		if row >= alignmentStart && row < alignmentStart+5 && col >= alignmentStart && col < alignmentStart+5 {
			return true
		}
	}
	return false
}

func readBits(data []byte, position *int, count int) (int, bool) {
	if *position+count > len(data)*8 {
		return 0, false
	}
	value := 0
	for i := 0; i < count; i++ {
		value <<= 1
		if data[*position/8]&(1<<uint(7-*position%8)) != 0 {
			value |= 1
		}
		*position++
	}
	return value, true
}
