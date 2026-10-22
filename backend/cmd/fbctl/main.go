// Command fbctl is a thin CLI channel over the same /v1 API the web UI uses.
// It proves the API is channel-agnostic: no UI-only endpoints, same auth, same errors.
//
//	export FLOWBOARD_URL=http://localhost:8080   FLOWBOARD_USER=rahul
//	fbctl tree
//	fbctl board lst_sprint
//	fbctl tasks lst_backlog --sort dueDate --cf component=backend
//	fbctl create lst_sprint "Ship it" component=backend story_points=3
//	fbctl status tsk_seed_10 in_progress
//	fbctl move tsk_seed_01 lst_sprint [--policy reject] [field=value ...]
//	fbctl token            (prints a short-lived JWT; then set FLOWBOARD_TOKEN)
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

var (
	base  = envOr("FLOWBOARD_URL", "http://localhost:8080")
	user  = envOr("FLOWBOARD_USER", "priya")
	token = os.Getenv("FLOWBOARD_TOKEN")
)

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func call(method, path string, body any) map[string]any {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, strings.TrimRight(base, "/")+"/v1"+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.Header.Set("X-User-Id", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		die(err.Error())
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 400 {
		b, _ := json.MarshalIndent(out, "", "  ")
		die(fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, b))
	}
	return out
}

func die(msg string) { fmt.Fprintln(os.Stderr, msg); os.Exit(1) }

// fieldArgs turns key=value pairs into JSON values (numbers, bools, a,b lists stay strings unless bracketed).
func fieldArgs(args []string) map[string]any {
	out := map[string]any{}
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok {
			die("expected key=value, got " + a)
		}
		var j any
		if err := json.Unmarshal([]byte(v), &j); err == nil {
			out[k] = j
		} else {
			out[k] = v
		}
	}
	return out
}

func printTree(n map[string]any, depth int) {
	fmt.Printf("%s%-7s %-22s %s %s\n", strings.Repeat("  ", depth), n["type"], n["name"], n["id"], n["visibility"])
	if kids, ok := n["children"].([]any); ok {
		for _, k := range kids {
			printTree(k.(map[string]any), depth+1)
		}
	}
}

func main() {
	if len(os.Args) < 2 {
		die("usage: fbctl tree|board|tasks|create|status|move|token ...")
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "token":
		out := call("POST", "/auth/token", map[string]string{"userId": user})
		fmt.Println(out["token"])
	case "tree":
		printTree(call("GET", "/tree", nil), 0)
	case "board":
		if len(args) < 1 {
			die("fbctl board <listId>")
		}
		b := call("GET", "/lists/"+args[0]+"/board", nil)
		for _, c := range b["columns"].([]any) {
			col := c.(map[string]any)
			st := col["status"].(map[string]any)
			fmt.Printf("== %s [%s] (%v)\n", st["name"], st["key"], col["count"])
			for _, t := range col["tasks"].([]any) {
				tk := t.(map[string]any)
				fmt.Printf("   %-14s %-8s %s\n", tk["id"], tk["priority"], tk["title"])
			}
		}
	case "tasks":
		if len(args) < 1 {
			die("fbctl tasks <listId> [--sort k] [--order asc|desc] [--cf key=value]")
		}
		q := url.Values{"limit": {"200"}}
		for i := 1; i+1 < len(args); i += 2 {
			switch args[i] {
			case "--sort":
				q.Set("sort", args[i+1])
			case "--order":
				q.Set("order", args[i+1])
			case "--cf":
				k, v, _ := strings.Cut(args[i+1], "=")
				q.Set("cf."+k, v)
			}
		}
		p := call("GET", "/lists/"+args[0]+"/tasks?"+q.Encode(), nil)
		for _, t := range p["data"].([]any) {
			tk := t.(map[string]any)
			due, _ := tk["dueDate"].(string)
			fmt.Printf("%-14s %-12s %-8s %-22s %s\n", tk["id"], tk["statusKey"], tk["priority"], due, tk["title"])
		}
	case "create":
		if len(args) < 2 {
			die("fbctl create <listId> <title> [field=value ...]")
		}
		t := call("POST", "/lists/"+args[0]+"/tasks", map[string]any{"title": args[1], "customFields": fieldArgs(args[2:])})
		fmt.Println("created", t["id"], "in", t["statusKey"])
	case "status":
		if len(args) < 2 {
			die("fbctl status <taskId> <statusKey>")
		}
		t := call("PATCH", "/tasks/"+args[0], map[string]any{"statusKey": args[1]})
		fmt.Println(t["id"], "->", t["statusKey"], "v"+strconv.Itoa(int(t["version"].(float64))))
	case "move":
		if len(args) < 2 {
			die("fbctl move <taskId> <listId> [--policy reject] [field=value ...]")
		}
		body := map[string]any{"listId": args[1]}
		rest := args[2:]
		if len(rest) >= 2 && rest[0] == "--policy" {
			body["statusPolicy"] = rest[1]
			rest = rest[2:]
		}
		if len(rest) > 0 {
			body["customFields"] = fieldArgs(rest)
		}
		res := call("POST", "/tasks/"+args[0]+"/move", body)
		b, _ := json.MarshalIndent(res["effects"], "", "  ")
		t := res["task"].(map[string]any)
		fmt.Printf("%s now in %s / %s\neffects: %s\n", t["id"], t["listId"], t["statusKey"], b)
	default:
		die("unknown command " + os.Args[1])
	}
}
