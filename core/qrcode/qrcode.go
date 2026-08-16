package qrcode

import (
	"bytes"
	"fmt"
)

// PrintTerminal outputs an ANSI colored or unicode block QR code for the given text to the console.
func PrintTerminal(text string) string {
	qr, err := Encode(text, Medium)
	if err != nil {
		return fmt.Sprintf("[URL: %s]\n", text)
	}
	return qr.ToSmallTerminalString()
}

// ECL represents Error Correction Level
type ECL int

const (
	Low ECL = iota
	Medium
	Quartile
	High
)

// QRCode represents a generated QR Code matrix
type QRCode struct {
	Size    int
	Modules [][]bool
}

// Encode generates a QRCode matrix for the given string
func Encode(text string, level ECL) (*QRCode, error) {
	data := []byte(text)
	// For LAN Drop URLs (approx 30-60 chars), Version 3 or 4 is sufficient.
	// We dynamically pick the minimal version needed.
	ver := pickVersion(len(data), level)
	if ver > 10 {
		ver = 10
	}

	size := 17 + 4*ver
	matrix := make([][]bool, size)
	reserved := make([][]bool, size)
	for i := range matrix {
		matrix[i] = make([]bool, size)
		reserved[i] = make([]bool, size)
	}

	// 1. Finder patterns (top-left, top-right, bottom-left)
	addFinderPattern(matrix, reserved, 0, 0)
	addFinderPattern(matrix, reserved, size-7, 0)
	addFinderPattern(matrix, reserved, 0, size-7)

	// 2. Timing patterns
	for i := 8; i < size-8; i++ {
		val := (i % 2) == 0
		setModule(matrix, reserved, 6, i, val)
		setModule(matrix, reserved, i, 6, val)
	}

	// 3. Dark module
	setModule(matrix, reserved, 8, 4*ver+9, true)

	// 4. Alignment patterns if ver >= 2
	if ver >= 2 {
		pos := alignmentPatternPositions(ver)
		for _, r := range pos {
			for _, c := range pos {
				if isFinderOverlap(r, c, size) {
					continue
				}
				addAlignmentPattern(matrix, reserved, r-2, c-2)
			}
		}
	}

	// 5. Reserve format info areas
	for i := 0; i < 9; i++ {
		reserved[8][i] = true
		reserved[i][8] = true
	}
	for i := 0; i < 8; i++ {
		reserved[8][size-1-i] = true
		reserved[size-1-i][8] = true
	}

	// 6. Encode Data & ECC
	bitStream := buildBitStream(data, ver, level)
	dataCodewords := bitsToBytes(bitStream)
	totalCapacity := totalDataCodewords(ver)
	padDataCodewords(&dataCodewords, totalCapacity)

	eccBytesPerBlock, numBlocks := getEccInfo(ver, level)
	eccBlocks := make([][]byte, numBlocks)
	dataBlocks := splitDataBlocks(dataCodewords, numBlocks)

	for i := 0; i < numBlocks; i++ {
		eccBlocks[i] = calculateReedSolomon(dataBlocks[i], eccBytesPerBlock)
	}

	// Interleave data and ECC
	finalBytes := interleave(dataBlocks, eccBlocks)
	finalBits := bytesToBits(finalBytes)

	// 7. Place data bits
	placeDataBits(matrix, reserved, finalBits)

	// 8. Masking & Best Mask Selection (default to mask pattern 0 for simplicity)
	maskPattern := 0
	applyMask(matrix, reserved, maskPattern)

	// 9. Write Format Information (ECL + Mask)
	writeFormatInfo(matrix, level, maskPattern)

	return &QRCode{Size: size, Modules: matrix}, nil
}

func (qr *QRCode) ToSmallTerminalString() string {
	var buf bytes.Buffer
	border := 2
	totalSize := qr.Size + border*2

	// Render using unicode half blocks: upper half ▀ (top dark, bottom light)
	// Each terminal line renders two vertical pixels.
	for y := 0; y < totalSize; y += 2 {
		for x := 0; x < totalSize; x++ {
			top := qr.getModuleWithBorder(x-border, y-border)
			bottom := false
			if y+1 < totalSize {
				bottom = qr.getModuleWithBorder(x-border, y+1-border)
			}

			if top && bottom {
				buf.WriteString("█") // Both black/dark
			} else if top && !bottom {
				buf.WriteString("▀") // Top dark, bottom light
			} else if !top && bottom {
				buf.WriteString("▄") // Top light, bottom dark
			} else {
				buf.WriteString(" ") // Both light
			}
		}
		buf.WriteString("\n")
	}
	return buf.String()
}

func (qr *QRCode) getModuleWithBorder(x, y int) bool {
	if x < 0 || x >= qr.Size || y < 0 || y >= qr.Size {
		return false
	}
	return qr.Modules[y][x]
}

func setModule(m, res [][]bool, r, c int, val bool) {
	m[r][c] = val
	res[r][c] = true
}

func addFinderPattern(m, res [][]bool, r, c int) {
	for i := -1; i <= 7; i++ {
		for j := -1; j <= 7; j++ {
			rr, cc := r+i, c+j
			if rr >= 0 && rr < len(m) && cc >= 0 && cc < len(m) {
				res[rr][cc] = true
				if i >= 0 && i <= 6 && j >= 0 && j <= 6 {
					if i == 0 || i == 6 || j == 0 || j == 6 || (i >= 2 && i <= 4 && j >= 2 && j <= 4) {
						m[rr][cc] = true
					} else {
						m[rr][cc] = false
					}
				} else {
					m[rr][cc] = false
				}
			}
		}
	}
}

func addAlignmentPattern(m, res [][]bool, r, c int) {
	for i := 0; i < 5; i++ {
		for j := 0; j < 5; j++ {
			res[r+i][c+j] = true
			if i == 0 || i == 4 || j == 0 || j == 4 || (i == 2 && j == 2) {
				m[r+i][c+j] = true
			} else {
				m[r+i][c+j] = false
			}
		}
	}
}

func isFinderOverlap(r, c, size int) bool {
	if r < 8 && c < 8 {
		return true
	}
	if r < 8 && c >= size-8 {
		return true
	}
	if r >= size-8 && c < 8 {
		return true
	}
	return false
}

func alignmentPatternPositions(ver int) []int {
	switch ver {
	case 2:
		return []int{6, 18}
	case 3:
		return []int{6, 22}
	case 4:
		return []int{6, 26}
	case 5:
		return []int{6, 30}
	case 6:
		return []int{6, 34}
	default:
		return []int{6, 6 + ver*4}
	}
}

func pickVersion(dataLen int, level ECL) int {
	capacities := []int{0, 14, 26, 42, 62, 84, 106, 122, 152, 180, 213}
	for v := 1; v <= 10; v++ {
		if dataLen <= capacities[v] {
			return v
		}
	}
	return 6
}

func totalDataCodewords(ver int) int {
	// Total data codewords (excluding ECC) for Medium ECL
	caps := []int{0, 16, 28, 44, 64, 86, 108, 124, 154, 182, 216}
	if ver < len(caps) {
		return caps[ver]
	}
	return 64
}

func getEccInfo(ver int, level ECL) (eccBytesPerBlock, numBlocks int) {
	switch ver {
	case 1:
		return 10, 1
	case 2:
		return 16, 1
	case 3:
		return 26, 1
	case 4:
		return 18, 2
	case 5:
		return 24, 2
	default:
		return 18, 2
	}
}

func buildBitStream(data []byte, ver int, level ECL) []int {
	var bits []int
	// Mode 8-bit byte: 0100
	bits = append(bits, 0, 1, 0, 0)
	// Character count indicator (8 bits for ver 1-9)
	for i := 7; i >= 0; i-- {
		bits = append(bits, (len(data)>>i)&1)
	}
	// Data bits
	for _, b := range data {
		for i := 7; i >= 0; i-- {
			bits = append(bits, int((b>>i)&1))
		}
	}
	// Terminator (up to 4 zeroes)
	for i := 0; i < 4; i++ {
		bits = append(bits, 0)
	}
	// Pad to multiple of 8
	for len(bits)%8 != 0 {
		bits = append(bits, 0)
	}
	return bits
}

func bitsToBytes(bits []int) []byte {
	var bytes []byte
	for i := 0; i < len(bits); i += 8 {
		var b byte
		for j := 0; j < 8 && i+j < len(bits); j++ {
			b = (b << 1) | byte(bits[i+j])
		}
		bytes = append(bytes, b)
	}
	return bytes
}

func padDataCodewords(data *[]byte, total int) {
	padBytes := []byte{0xEC, 0x11}
	padIdx := 0
	for len(*data) < total {
		*data = append(*data, padBytes[padIdx])
		padIdx = (padIdx + 1) % 2
	}
}

func splitDataBlocks(data []byte, numBlocks int) [][]byte {
	blocks := make([][]byte, numBlocks)
	blockSize := len(data) / numBlocks
	for i := 0; i < numBlocks; i++ {
		start := i * blockSize
		end := start + blockSize
		if i == numBlocks-1 {
			end = len(data)
		}
		blocks[i] = data[start:end]
	}
	return blocks
}

// GF(256) Math for Reed-Solomon
var gfExp [512]byte
var gfLog [256]byte

func init() {
	var x byte = 1
	for i := 0; i < 255; i++ {
		gfExp[i] = x
		gfExp[i+255] = x
		gfLog[x] = byte(i)
		x = gfMulRaw(x, 0x02)
	}
}

func gfMulRaw(x, y byte) byte {
	var r byte
	for i := 0; i < 8; i++ {
		if (y & 1) != 0 {
			r ^= x
		}
		hi := (x & 0x80) != 0
		x <<= 1
		if hi {
			x ^= 0x1D // Polynomial x^8 + x^4 + x^3 + x^2 + 1
		}
		y >>= 1
	}
	return r
}

func gfMul(x, y byte) byte {
	if x == 0 || y == 0 {
		return 0
	}
	return gfExp[int(gfLog[x])+int(gfLog[y])]
}

func calculateReedSolomon(data []byte, eccCount int) []byte {
	// Build generator polynomial
	gen := []byte{1}
	for i := 0; i < eccCount; i++ {
		next := []byte{1, gfExp[i]}
		gen = polyMul(gen, next)
	}

	res := make([]byte, len(data)+eccCount)
	copy(res, data)

	for i := 0; i < len(data); i++ {
		coef := res[i]
		if coef != 0 {
			for j := 0; j < len(gen); j++ {
				res[i+j] ^= gfMul(gen[j], coef)
			}
		}
	}
	return res[len(data):]
}

func polyMul(p1, p2 []byte) []byte {
	res := make([]byte, len(p1)+len(p2)-1)
	for i, c1 := range p1 {
		for j, c2 := range p2 {
			res[i+j] ^= gfMul(c1, c2)
		}
	}
	return res
}

func interleave(dataBlocks, eccBlocks [][]byte) []byte {
	var result []byte
	maxData := 0
	for _, b := range dataBlocks {
		if len(b) > maxData {
			maxData = len(b)
		}
	}
	for i := 0; i < maxData; i++ {
		for _, b := range dataBlocks {
			if i < len(b) {
				result = append(result, b[i])
			}
		}
	}
	maxEcc := len(eccBlocks[0])
	for i := 0; i < maxEcc; i++ {
		for _, b := range eccBlocks {
			if i < len(b) {
				result = append(result, b[i])
			}
		}
	}
	return result
}

func bytesToBits(data []byte) []int {
	var bits []int
	for _, b := range data {
		for i := 7; i >= 0; i-- {
			bits = append(bits, int((b>>i)&1))
		}
	}
	return bits
}

func placeDataBits(m, res [][]bool, bits []int) {
	size := len(m)
	bitIdx := 0
	upwards := true

	for col := size - 1; col > 0; col -= 2 {
		if col == 6 {
			col--
		}
		for row := 0; row < size; row++ {
			r := row
			if upwards {
				r = size - 1 - row
			}
			for c := col; c >= col-1; c-- {
				if !res[r][c] {
					if bitIdx < len(bits) {
						m[r][c] = (bits[bitIdx] == 1)
						bitIdx++
					}
				}
			}
		}
		upwards = !upwards
	}
}

func applyMask(m, res [][]bool, mask int) {
	size := len(m)
	for r := 0; r < size; r++ {
		for c := 0; c < size; c++ {
			if res[r][c] {
				continue
			}
			invert := false
			switch mask {
			case 0:
				invert = (r+c)%2 == 0
			case 1:
				invert = r%2 == 0
			case 2:
				invert = c%3 == 0
			case 3:
				invert = (r+c)%3 == 0
			}
			if invert {
				m[r][c] = !m[r][c]
			}
		}
	}
}

func writeFormatInfo(m [][]bool, level ECL, mask int) {
	// Format Info: ECL Medium (00) + Mask 0 (000) = 00000 -> with BCH = 101010000010010 ^ 101010000010010 = 0
	// Precomputed format bits for Medium ECL, Mask 0: 0x5412 (101010000010010)
	formatBits := 0x5412 ^ 0x5412 // after mask = 0 (15 bits 0)
	if mask == 0 && level == Medium {
		formatBits = 0x5412 ^ 0x5412
	}
	bits := make([]bool, 15)
	for i := 0; i < 15; i++ {
		bits[i] = ((formatBits >> (14 - i)) & 1) == 1
	}

	size := len(m)
	// Write to top-left
	order1 := [][2]int{
		{8, 0}, {8, 1}, {8, 2}, {8, 3}, {8, 4}, {8, 5}, {8, 7}, {8, 8},
		{7, 8}, {5, 8}, {4, 8}, {3, 8}, {2, 8}, {1, 8}, {0, 8},
	}
	for i, pos := range order1 {
		m[pos[0]][pos[1]] = bits[i]
	}

	// Write to split areas
	order2 := [][2]int{
		{size - 1, 8}, {size - 2, 8}, {size - 3, 8}, {size - 4, 8}, {size - 5, 8}, {size - 6, 8}, {size - 7, 8},
		{8, size - 8}, {8, size - 7}, {8, size - 6}, {8, size - 5}, {8, size - 4}, {8, size - 3}, {8, size - 2}, {8, size - 1},
	}
	for i, pos := range order2 {
		m[pos[0]][pos[1]] = bits[i]
	}
}
