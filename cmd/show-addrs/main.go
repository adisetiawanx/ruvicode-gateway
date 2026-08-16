package main
import (
    "fmt"
    "os"
    "github.com/ruvicode/gateway/internal/wallet"
)
func main() {
    hd, _ := wallet.NewFromMnemonic(os.Args[1])
    for _, i := range []uint32{200,201,202,203,204} {
        a, _, _ := hd.DeriveAddress(i)
        fmt.Printf("%d %s\n", i, a.Hex())
    }
}
