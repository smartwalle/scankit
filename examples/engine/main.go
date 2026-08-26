package main

import (
	"bytes"
	"fmt"
	"log"

	"github.com/smartwalle/scankit"
)

func main() {
	var engine, err = scankit.New([]scankit.Expression{
		{
			Id:      1,
			Pattern: `(?:\b|^)(?:\+86|86)?1[3-9]\d{9}(?:\b|$)`,
		},
		{
			Id:      2,
			Pattern: "[A-Za-z0-9.!#$%&'*+/?^_`{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\\b",
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	var msg = []byte("ts=2026-08-19 level=info user=42 phone=13800138000 12345678@qq.com email=alice.smith42@example.cn invalid=12345678901 path=/api/profile")

	engine.Mask(msg, func(match scankit.Match, value []byte) {
		switch match.Id {
		case 1:
			for i := 3; i < 7; i++ {
				value[i] = '*'
			}
		case 2:
			for i := 3; i < min(bytes.IndexByte(value, '@'), 7); i++ {
				value[i] = '*'
			}
		}
	})
	fmt.Println(string(msg))
}
