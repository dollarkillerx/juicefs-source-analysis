package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/dollarkillerx/urllib"
)

const url = "http://81.71.93.124:8386/search"

func main() {
	open, err := os.Open("sp.csv")
	if err != nil {
		log.Fatalln(err)
	}
	defer open.Close()

	reader := csv.NewReader(open)

	create, err := os.Create("sp_back.csv")
	if err != nil {
		log.Fatalln(err)
	}
	writer := csv.NewWriter(create)
	defer writer.Flush()


	i, bytes, err := urllib.Post(url).SetJson([]byte(`
{
    "time_range": "2021_04_01~2021_05_27"
}
`)).Byte()

	if err != nil {
		log.Fatalln(err)
	}
	fmt.Println(i)

	var ts Ts

	err = json.Unmarshal(bytes, &ts)
	if err != nil {
		log.Fatalln(err)
	}

	mpDir := map[string]struct{}{}

	for _,v := range ts.Time {
		for vv := range v.Source {
			mpDir[vv] = struct{}{}
		}
	}

	for {
		read, err := reader.Read()
		if err != nil {
			break
		}
		key := strings.TrimSpace(read[0])

		_, bytes, err := urllib.Post(url).SetJson([]byte(fmt.Sprintf(`
{
    "source": "%s"
}
`, key))).Byte()
		if err != nil {
			fmt.Println(err)
		}
		var tts Ts
		err = json.Unmarshal(bytes, &tts)
		if err != nil {
			log.Fatalln(err)
		}

		read[1] = strconv.Itoa(tts.Total)

		_,ex := mpDir[key]
		if ex {
			read[3] = "无异常"
		}

		writer.Write(read)
	}

	//fmt.Println(ts.Total)
}

type Ts struct {
	Total int `json:"total"`
	Time map[string]Item `json:"time"`
}

type Item struct {
	Source map[string]interface{} `json:"source"`
}

//type ItemInn struct {
//	Source
//}