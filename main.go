package main

import (
	"fmt"

	"github.com/aalhour/beachdb/internal/util/checksum"
)

func main() {
	data := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 101, 234}
	sum := checksum.CRC32C(data)
	fmt.Printf("CRC32C of %q = 0x%x\n", data, sum)
}
