package main

import (
	"fmt"
	"log"

	"github.com/smartwalle/scankit"
)

func main() {
	var scanner, err = scankit.Compile([]scankit.Expression{
		{
			Id:      1,
			Pattern: "1[3-9][0-9]{9}",
		},
		{
			Id:      2,
			Pattern: "[A-Za-z0-9.!#$%&'*+/?^_`{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\\b",
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	var msg = []byte("ts=2026-08-19 level=info user=42 phone=13800138000 email=alice.smith42@example.cn invalid=12345678901 path=/api/profile")

	matches, err := scanner.Scan(msg)
	if err != nil {
		log.Fatal(err)
	}
	for _, match := range matches {
		fmt.Printf("命中规则=%d，号码=%s，范围=[%d,%d)\n",
			match.Id,
			msg[match.From:match.To],
			match.From,
			match.To,
		)
	}
}
