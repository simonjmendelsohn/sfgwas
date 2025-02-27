package general

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/hcholab/sfgwas/crypto"
	"github.com/ldsec/lattigo/v2/ckks"
	"github.com/ldsec/lattigo/v2/ring"
	"go.dedis.ch/onet/v3/log"
)

type DiagCacheStream struct {
	filename string
	file     *os.File
	reader   *bufio.Reader
	writer   *bufio.Writer
	buf      []byte
	atHead   bool

	vectorLen    uint64
	level        uint64
	scale        float64
	n            uint64
	numModuli    uint64
	rowSize      uint64
	cryptoParams *crypto.CryptoParams

	babyTable  []bool
	giantTable []bool

	resetPosition int
}

// Second output is a flag indicating whether write is attempted when the file already exists
func NewDiagCacheStream(cryptoParams *crypto.CryptoParams, filePrefix string, blockRowIndex int, isWrite bool) (*DiagCacheStream, bool) {
	filename := fmt.Sprintf("%s_%d.bin", filePrefix, blockRowIndex)

	var file *os.File
	var err error
	if isWrite {
		if _, err := os.Stat(filename); os.IsNotExist(err) {
			file, err = os.Create(filename)
			if err != nil {
				panic(err)
			}
			log.LLvl1(time.Now().Format(time.RFC3339), "Created cache file:", filename)
		} else {
			log.LLvl1(time.Now().Format(time.RFC3339), "Found cache file:", filename)
			return nil, true
		}
	} else {
		file, err = os.Open(filename)
		log.LLvl1(time.Now().Format(time.RFC3339), "Opened cache file:", filename)
	}
	if err != nil {
		panic(err)
	}

	var reader *bufio.Reader
	var writer *bufio.Writer
	var buf []byte
	var vectorLen, level, n, numModuli, rowSize uint64
	var babyTable, giantTable []bool
	var scale float64
	var resetPosition int

	d := int(math.Ceil(math.Sqrt(float64(cryptoParams.GetSlots()))))

	if isWrite {
		writer = bufio.NewWriter(file)
	} else {
		reader = bufio.NewReader(file)

		headerBuf := make([]byte, 8*6) // If this is changed, also update ResetRead()
		_, err = io.ReadFull(reader, headerBuf)
		if err != nil {
			panic(err)
		}

		vectorLen = binary.LittleEndian.Uint64(headerBuf)
		level = binary.LittleEndian.Uint64(headerBuf[8:])
		scale = math.Float64frombits(binary.LittleEndian.Uint64(headerBuf[16:]))
		n = binary.LittleEndian.Uint64(headerBuf[24:])
		numModuli = binary.LittleEndian.Uint64(headerBuf[32:])
		rowSize = binary.LittleEndian.Uint64(headerBuf[40:])

		babyTable = make([]bool, d)
		giantTable = make([]bool, d)

		tableBuf := make([]byte, d*2)
		_, err = io.ReadFull(reader, tableBuf)
		if err != nil {
			panic(err)
		}

		for i := 0; i < d; i++ {
			babyTable[i] = tableBuf[i] != 0
			giantTable[i] = tableBuf[d+i] != 0
		}

		fmt.Println("DiagCacheStream header:")
		fmt.Println(vectorLen, level, scale, n, numModuli, rowSize)
		fmt.Println(d, babyTable[:10])
		fmt.Println(d, giantTable[:10])

		resetPosition = len(headerBuf) + len(tableBuf)

		buf = make([]byte, rowSize)
	}

	return &DiagCacheStream{
		filename:      filename,
		buf:           buf,
		file:          file,
		reader:        reader,
		writer:        writer,
		atHead:        true,
		vectorLen:     vectorLen,
		level:         level,
		scale:         scale,
		n:             n,
		numModuli:     numModuli,
		rowSize:       rowSize,
		cryptoParams:  cryptoParams,
		babyTable:     babyTable,
		giantTable:    giantTable,
		resetPosition: resetPosition,
	}, false
}

func (dcs *DiagCacheStream) SetIndexTables(babyTable, giantTable []bool) {
	dcs.babyTable = babyTable
	dcs.giantTable = giantTable
}

func (dcs *DiagCacheStream) GetIndexTables() ([]bool, []bool) {
	return dcs.babyTable, dcs.giantTable
}

func (dcs *DiagCacheStream) WriteDiag(pv crypto.PlainVector, shift uint32) {

	if dcs.atHead { // Write header
		dcs.vectorLen = uint64(len(pv))
		dcs.level = uint64(pv[0].Level())
		dcs.scale = pv[0].Scale()
		dcs.n = uint64(pv[0].Value()[0].Degree())
		dcs.numModuli = uint64(pv[0].Value()[0].LenModuli())
		dcs.rowSize = 4 + (uint64(1+pv[0].Value()[0].GetDataLen(false)) * dcs.vectorLen)

		buf := make([]byte, 8*6)

		binary.LittleEndian.PutUint64(buf, dcs.vectorLen)
		binary.LittleEndian.PutUint64(buf[8:], dcs.level)
		binary.LittleEndian.PutUint64(buf[16:], math.Float64bits(dcs.scale))
		binary.LittleEndian.PutUint64(buf[24:], dcs.n)
		binary.LittleEndian.PutUint64(buf[32:], dcs.numModuli)
		binary.LittleEndian.PutUint64(buf[40:], dcs.rowSize)

		_, err := dcs.writer.Write(buf)
		if err != nil {
			log.Fatal(err)
		}

		if dcs.babyTable == nil || dcs.giantTable == nil {
			log.Fatal("babyTable or giantTable not set before attempting to write diag cache header")
		}

		tableBuf := make([]byte, len(dcs.babyTable)+len(dcs.giantTable))

		conv := func(b bool) byte {
			if b {
				return 1
			} else {
				return 0
			}
		}
		for i, v := range dcs.babyTable {
			tableBuf[i] = conv(v)
		}
		pos := len(dcs.babyTable)
		for i, v := range dcs.giantTable {
			tableBuf[pos+i] = conv(v)
		}

		_, err = dcs.writer.Write(tableBuf)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println("Written DiagCacheStream header:")
		fmt.Println(dcs.vectorLen, dcs.level, dcs.scale, dcs.n, dcs.numModuli, dcs.rowSize)
		fmt.Println(len(dcs.babyTable), dcs.babyTable[:10])
		fmt.Println(len(dcs.giantTable), dcs.giantTable[:10])

		dcs.buf = make([]byte, dcs.rowSize)
		dcs.atHead = false
	}

	binary.LittleEndian.PutUint32(dcs.buf[:4], shift)

	var err error
	pointer := uint64(4)
	for i := range pv {
		isEmpty := uint8(0)
		if pv[i] == nil {
			isEmpty = 1
		}
		dcs.buf[pointer] = isEmpty
		pointer++

		if isEmpty == 0 {
			var tmp int
			tmp, err = ring.WriteCoeffsTo(int(pointer), int(dcs.n), int(dcs.numModuli), pv[i].Value()[0].Coeffs, dcs.buf)
			pointer = uint64(tmp)
			if err != nil {
				panic(err)
			}
		}
	}

	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, pointer)
	dcs.writer.Write(buf)
	dcs.writer.Write(dcs.buf[:pointer])
}

func (dcs *DiagCacheStream) Close() {
	if dcs.writer != nil {
		dcs.writer.Flush()
	}
	dcs.writer = nil
	dcs.reader = nil
	dcs.file.Close()
}

func (dcs *DiagCacheStream) ResetRead() {
	dcs.file.Seek(int64(dcs.resetPosition), io.SeekStart) // Skip header
	dcs.reader = bufio.NewReader(dcs.file)
}

func (dcs *DiagCacheStream) ReadDiag() (pv crypto.PlainVector, shift int) {

	buf := make([]byte, 8)
	_, err := io.ReadFull(dcs.reader, buf)
	if err != nil {
		return nil, 0
	}

	bytesToRead := binary.LittleEndian.Uint64(buf)
	_, err = io.ReadFull(dcs.reader, dcs.buf[:bytesToRead])
	if err != nil {
		return nil, 0
	}

	shift = int(binary.LittleEndian.Uint32(dcs.buf[:4]))

	pointer := uint64(4)

	pv = make(crypto.PlainVector, dcs.vectorLen)
	for i := range pv {
		isEmpty := dcs.buf[pointer] == 1
		pointer++

		if !isEmpty {
			pt := ckks.NewPlaintext(dcs.cryptoParams.Params, int(dcs.level), dcs.scale)
			for i := range pt.Value() {
				var tmp int
				tmp, _ = ring.DecodeCoeffs(int(pointer), int(dcs.n), int(dcs.numModuli), pt.Value()[i].Coeffs, dcs.buf[:bytesToRead])
				pointer = uint64(tmp)
			}
			pv[i] = pt
		}
	}

	return pv, shift
}
