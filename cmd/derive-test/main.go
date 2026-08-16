package main
import (
    "fmt"
    "os"
    "github.com/ruvicode/gateway/internal/wallet"
)
func main() {
    hd, err := wallet.NewFromMnemonic(os.Args[1])
    if err != nil { panic(err) }
    for i := uint32(0); i < 3; i++ {
        addr, _, err := hd.DeriveAddress(i)
        if err != nil { panic(err) }
        fmt.Printf("index %d: %s\n", i, addr.Hex())
    }
}
