package util

import (
    "mozilla.org/simplepush/storage"
    "log"
    "io"
    "os"
    "bufio"
    "strings"
)


func MzGetConfig(filename string) (storage.JsMap) {
    config := make(storage.JsMap)
    // Yay for no equivalent to readln
    file, err := os.Open(filename)
    if err != nil {
        log.Fatal (err)
    }
    reader := bufio.NewReader(file)
    for line, err := reader.ReadString('\n');
        err == nil;
        line, err = reader.ReadString('\n') {
        // skip lines beginning with '#/;'
        if strings.Contains("#/;", string(line[0])){
            continue
        }
        kv := strings.SplitN(line, "=", 2)
        if len(kv) < 2 {
            continue
        }
        config[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
    }
    if err != nil && err != io.EOF {
        log.Panic(err)
    }
    return config
}

func MzGet(ma storage.JsMap, key string, def string) (string) {
    val, ok := ma[key].(string)
    if ! ok {
        val = def
    }
    return val
}


