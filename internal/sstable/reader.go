package sstable

import (
	"os"

	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

// Reader defines the SSTable reader struct.
type Reader struct {
	file     *os.File
	fileSize int64
	footer   footer
	index    []indexEntry
}

// OpenReader reads an SSTable file and creates a Reader struct with
// the footer and index entries read, decoded and checksum-verified
func OpenReader(file *os.File) (*Reader, error) {
	// Validate file ptr and size
	if file == nil {
		return nil, ErrNilFile
	}

	info, err := file.Stat()
	if err != nil {
		return nil, ErrReadingFile
	}

	fileSize := info.Size()
	if fileSize < int64(footerSize) {
		return nil, ErrFileTooSmall
	}

	// Read the footer
	decodedFooter, err := readFooter(file)
	if err != nil {
		return nil, err
	}

	// Read index entries from footer's indexOffset
	indexEntries, err := readIndexEntries(file, decodedFooter)

	// Construct and return the reader
	reader := &Reader{
		file:     file,
		fileSize: fileSize,
		footer:   decodedFooter,
		index:    indexEntries,
	}

	return reader, nil
}

// readFooter takes a file and returns a footer struct or error
// this function assumes that the file pointer was validated
func readFooter(file *os.File) (decodedFooter footer, err error) {
	// Seek to the footerSize with whence=2 (relative to EOF)
	fileOffset, err := file.Seek(int64(footerSize), 2)
	if err != nil {
		return footer{}, ErrReadingFile
	}

	// Read the `footerSize` number of bytes
	footerPayload := make([]byte, footerSize)
	numBytesRead, err := file.ReadAt(footerPayload, fileOffset)
	if numBytesRead != int(footerSize) {
		return footer{}, ErrFileTooSmall
	}

	// Decode the byte buffer
	decodedFooter, err = decodeFooter(footerPayload)
	if err != nil {
		return footer{}, err
	}

	return decodedFooter, nil
}

func (r *Reader) Close() error {

}

// readIndexEntries takes a file and a decoded footer struct and reads the
// the full index bytes, returning an indexEntries slice or error
// this function assumes that the file pointer was validated
func readIndexEntries(file *os.File, decodedFooter footer) ([]indexEntry, error) {
	// Seek to indexOffset and decode the index entries
	indexOffset, err := file.Seek(int64(decodedFooter.indexOffset), 1)
	if err != nil {
		return nil, ErrReadingFile
	}
	indexPayload := make([]byte, decodedFooter.indexSize)
	numBytesRead, err := file.ReadAt(indexPayload, indexOffset)
	if numBytesRead != int(decodedFooter.indexSize) || err != nil {
		return nil, ErrCorruptIndex
	}

	// Validate the CRC32 checksum
	n := len(indexPayload)
	expectedCRC32 := coding.Uint32(indexPayload[n-4:])
	calculatedCRC32 := checksum.CRC32C(indexPayload[:n-4])
	if calculatedCRC32 != expectedCRC32 {
		return nil, ErrCorruptIndex
	}

	var indexEntries []indexEntry
	br := coding.NewByteReader(indexPayload)

	for br.Remaining() > 0 {
		keyLen, err := br.ReadUint32()
		if err != nil {
			return nil, ErrCorruptIndex
		}

		keyBytes, err := br.ReadBytes(int(keyLen))
		if err != nil {
			return nil, ErrCorruptIndex
		}

		key, err := keys.DecodeInternalKey(keyBytes)
		if err != nil {
			return nil, err
		}

		offset, err := br.ReadUint64()
		if err != nil {
			return nil, ErrCorruptIndex
		}

		size, err := br.ReadUint32()
		if err != nil {
			return nil, ErrCorruptIndex
		}

		indexEntries = append(indexEntries, indexEntry{
			lastKey: key,
			offset:  offset,
			size:    size,
		})
	}

	return indexEntries, nil
}
