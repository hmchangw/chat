package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

const (
	displayWidth  = 1024
	displayHeight = 768
	maxIterations = 10
)

func main() {
	client := anthropic.NewClient()

	task := "Take a screenshot and describe what you see on the screen."
	if len(os.Args) > 1 {
		task = strings.Join(os.Args[1:], " ")
	}

	fmt.Printf("Task: %s\n\n", task)

	messages := []anthropic.BetaMessageParam{
		anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(task)),
	}

	tools := []anthropic.BetaToolUnionParam{
		{OfComputerUseTool20251124: &anthropic.BetaToolComputerUse20251124Param{
			DisplayWidthPx:  displayWidth,
			DisplayHeightPx: displayHeight,
			DisplayNumber:   anthropic.Int(1),
		}},
		{OfTextEditor20250728: &anthropic.BetaToolTextEditor20250728Param{}},
		{OfBashTool20250124: &anthropic.BetaToolBash20250124Param{}},
	}

	for i := range maxIterations {
		fmt.Printf("[iteration %d]\n", i+1)

		resp, err := client.Beta.Messages.New(context.Background(), anthropic.BetaMessageNewParams{
			Model:     anthropic.ModelClaudeOpus4_6,
			MaxTokens: 4096,
			Tools:     tools,
			Messages:  messages,
			Betas:     []anthropic.AnthropicBeta{"computer-use-2025-11-24"},
		})
		if err != nil {
			log.Fatalf("API error: %v", err)
		}

		// Append assistant response to history
		assistantBlocks := make([]anthropic.BetaContentBlockParamUnion, 0, len(resp.Content))
		for _, block := range resp.Content {
			switch v := block.AsAny().(type) {
			case anthropic.BetaTextBlock:
				fmt.Printf("Assistant: %s\n", v.Text)
				assistantBlocks = append(assistantBlocks, anthropic.BetaContentBlockParamUnion{
					OfText: &anthropic.BetaTextBlockParam{Text: v.Text},
				})
			case anthropic.BetaToolUseBlock:
				inputJSON, _ := json.Marshal(v.Input)
				assistantBlocks = append(assistantBlocks, anthropic.BetaContentBlockParamUnion{
					OfToolUse: &anthropic.BetaToolUseBlockParam{
						ID:    v.ID,
						Name:  v.Name,
						Input: json.RawMessage(inputJSON),
					},
				})
			case anthropic.BetaThinkingBlock:
				assistantBlocks = append(assistantBlocks, anthropic.BetaContentBlockParamUnion{
					OfThinking: &anthropic.BetaThinkingBlockParam{
						Thinking:  v.Thinking,
						Signature: v.Signature,
					},
				})
			}
		}
		messages = append(messages, anthropic.BetaMessageParam{
			Role:    anthropic.BetaMessageParamRoleAssistant,
			Content: assistantBlocks,
		})

		if resp.StopReason != anthropic.BetaStopReasonToolUse {
			break
		}

		// Execute tool calls and collect results
		toolResults := []anthropic.BetaContentBlockParamUnion{}
		for _, block := range resp.Content {
			toolUse, ok := block.AsAny().(anthropic.BetaToolUseBlock)
			if !ok {
				continue
			}

			fmt.Printf("Tool: %s\n", toolUse.Name)

			var resultContent []anthropic.BetaToolResultBlockParamContentUnion
			var isError bool

			switch toolUse.Name {
			case "computer":
				resultContent, isError = executeComputerTool(toolUse.Input)
			case "bash":
				resultContent, isError = executeBashTool(toolUse.Input)
			case "str_replace_based_edit_tool":
				resultContent, isError = executeTextEditorTool(toolUse.Input)
			default:
				resultContent = []anthropic.BetaToolResultBlockParamContentUnion{
					{OfText: &anthropic.BetaTextBlockParam{Text: fmt.Sprintf("Unknown tool: %s", toolUse.Name)}},
				}
				isError = true
			}

			toolResultBlock := anthropic.BetaToolResultBlockParam{
				ToolUseID: toolUse.ID,
				Content:   resultContent,
			}
			if isError {
				toolResultBlock.IsError = anthropic.Bool(true)
			}

			toolResults = append(toolResults, anthropic.BetaContentBlockParamUnion{
				OfToolResult: &toolResultBlock,
			})
		}

		if len(toolResults) > 0 {
			messages = append(messages, anthropic.NewBetaUserMessage(toolResults...))
		}
	}
}

// executeComputerTool handles computer use actions (screenshot, mouse, keyboard).
func executeComputerTool(input any) ([]anthropic.BetaToolResultBlockParamContentUnion, bool) {
	inputBytes, err := json.Marshal(input)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal input: %v", err)), true
	}

	var params map[string]any
	if err := json.Unmarshal(inputBytes, &params); err != nil {
		return errorResult(fmt.Sprintf("failed to parse input: %v", err)), true
	}

	action, _ := params["action"].(string)
	fmt.Printf("  action: %s\n", action)

	switch action {
	case "screenshot":
		return takeScreenshot()
	case "left_click":
		return mouseAction("click", params, "1")
	case "right_click":
		return mouseAction("click", params, "3")
	case "middle_click":
		return mouseAction("click", params, "2")
	case "double_click":
		return mouseDoubleClick(params)
	case "triple_click":
		return mouseTripleClick(params)
	case "mouse_move":
		return mouseMove(params)
	case "left_click_drag":
		return mouseDrag(params)
	case "scroll":
		return mouseScroll(params)
	case "left_mouse_down":
		return xdotoolRun("mousedown", params, "1")
	case "left_mouse_up":
		return xdotoolRun("mouseup", params, "1")
	case "type":
		text, _ := params["text"].(string)
		if err := runCmd("xdotool", "type", "--clearmodifiers", "--", text); err != nil {
			return errorResult(fmt.Sprintf("type failed: %v", err)), true
		}
		return takeScreenshot()
	case "key":
		text, _ := params["text"].(string)
		if err := runCmd("xdotool", "key", "--clearmodifiers", text); err != nil {
			return errorResult(fmt.Sprintf("key failed: %v", err)), true
		}
		return takeScreenshot()
	case "hold_key":
		text, _ := params["text"].(string)
		if err := runCmd("xdotool", "keydown", text); err != nil {
			return errorResult(fmt.Sprintf("hold_key failed: %v", err)), true
		}
		return takeScreenshot()
	case "wait":
		return textResult("Waited."), false
	case "zoom":
		// zoom is not commonly supported without extra libraries; fall back to screenshot
		return takeScreenshot()
	default:
		return errorResult(fmt.Sprintf("unknown action: %s", action)), true
	}
}

func mouseAction(cmd string, params map[string]any, button string) ([]anthropic.BetaToolResultBlockParamContentUnion, bool) {
	coords, ok := params["coordinate"].([]any)
	if !ok || len(coords) < 2 {
		return errorResult("missing coordinate"), true
	}
	x := int(toFloat(coords[0]))
	y := int(toFloat(coords[1]))

	if err := runCmd("xdotool", "mousemove", "--sync", fmt.Sprintf("%d", x), fmt.Sprintf("%d", y)); err != nil {
		return errorResult(fmt.Sprintf("mousemove failed: %v", err)), true
	}
	if err := runCmd("xdotool", cmd, "--clearmodifiers", button); err != nil {
		return errorResult(fmt.Sprintf("%s failed: %v", cmd, err)), true
	}
	return takeScreenshot()
}

func mouseDoubleClick(params map[string]any) ([]anthropic.BetaToolResultBlockParamContentUnion, bool) {
	coords, ok := params["coordinate"].([]any)
	if !ok || len(coords) < 2 {
		return errorResult("missing coordinate"), true
	}
	x := int(toFloat(coords[0]))
	y := int(toFloat(coords[1]))

	if err := runCmd("xdotool", "mousemove", "--sync", fmt.Sprintf("%d", x), fmt.Sprintf("%d", y)); err != nil {
		return errorResult(fmt.Sprintf("mousemove failed: %v", err)), true
	}
	if err := runCmd("xdotool", "click", "--clearmodifiers", "--repeat", "2", "1"); err != nil {
		return errorResult(fmt.Sprintf("double click failed: %v", err)), true
	}
	return takeScreenshot()
}

func mouseTripleClick(params map[string]any) ([]anthropic.BetaToolResultBlockParamContentUnion, bool) {
	coords, ok := params["coordinate"].([]any)
	if !ok || len(coords) < 2 {
		return errorResult("missing coordinate"), true
	}
	x := int(toFloat(coords[0]))
	y := int(toFloat(coords[1]))

	if err := runCmd("xdotool", "mousemove", "--sync", fmt.Sprintf("%d", x), fmt.Sprintf("%d", y)); err != nil {
		return errorResult(fmt.Sprintf("mousemove failed: %v", err)), true
	}
	if err := runCmd("xdotool", "click", "--clearmodifiers", "--repeat", "3", "1"); err != nil {
		return errorResult(fmt.Sprintf("triple click failed: %v", err)), true
	}
	return takeScreenshot()
}

func mouseMove(params map[string]any) ([]anthropic.BetaToolResultBlockParamContentUnion, bool) {
	coords, ok := params["coordinate"].([]any)
	if !ok || len(coords) < 2 {
		return errorResult("missing coordinate"), true
	}
	x := int(toFloat(coords[0]))
	y := int(toFloat(coords[1]))

	if err := runCmd("xdotool", "mousemove", fmt.Sprintf("%d", x), fmt.Sprintf("%d", y)); err != nil {
		return errorResult(fmt.Sprintf("mousemove failed: %v", err)), true
	}
	return takeScreenshot()
}

func mouseDrag(params map[string]any) ([]anthropic.BetaToolResultBlockParamContentUnion, bool) {
	startCoords, ok := params["start_coordinate"].([]any)
	if !ok || len(startCoords) < 2 {
		return errorResult("missing start_coordinate"), true
	}
	endCoords, ok := params["coordinate"].([]any)
	if !ok || len(endCoords) < 2 {
		return errorResult("missing coordinate"), true
	}

	sx := int(toFloat(startCoords[0]))
	sy := int(toFloat(startCoords[1]))
	ex := int(toFloat(endCoords[0]))
	ey := int(toFloat(endCoords[1]))

	if err := runCmd("xdotool", "mousemove", fmt.Sprintf("%d", sx), fmt.Sprintf("%d", sy),
		"mousedown", "1",
		"mousemove", fmt.Sprintf("%d", ex), fmt.Sprintf("%d", ey),
		"mouseup", "1"); err != nil {
		return errorResult(fmt.Sprintf("drag failed: %v", err)), true
	}
	return takeScreenshot()
}

func mouseScroll(params map[string]any) ([]anthropic.BetaToolResultBlockParamContentUnion, bool) {
	coords, ok := params["coordinate"].([]any)
	if !ok || len(coords) < 2 {
		return errorResult("missing coordinate"), true
	}
	x := int(toFloat(coords[0]))
	y := int(toFloat(coords[1]))

	direction, _ := params["scroll_direction"].(string)
	amount := 3
	if a, ok := params["scroll_amount"].(float64); ok {
		amount = int(a)
	}

	if err := runCmd("xdotool", "mousemove", fmt.Sprintf("%d", x), fmt.Sprintf("%d", y)); err != nil {
		return errorResult(fmt.Sprintf("mousemove failed: %v", err)), true
	}

	button := "4" // up
	if direction == "down" {
		button = "5"
	} else if direction == "left" {
		button = "6"
	} else if direction == "right" {
		button = "7"
	}

	args := []string{"click", "--clearmodifiers", "--repeat", fmt.Sprintf("%d", amount), button}
	if err := runCmd("xdotool", args...); err != nil {
		return errorResult(fmt.Sprintf("scroll failed: %v", err)), true
	}
	return takeScreenshot()
}

func xdotoolRun(cmd string, params map[string]any, button string) ([]anthropic.BetaToolResultBlockParamContentUnion, bool) {
	coords, ok := params["coordinate"].([]any)
	if ok && len(coords) >= 2 {
		x := int(toFloat(coords[0]))
		y := int(toFloat(coords[1]))
		_ = runCmd("xdotool", "mousemove", fmt.Sprintf("%d", x), fmt.Sprintf("%d", y))
	}
	if err := runCmd("xdotool", cmd, button); err != nil {
		return errorResult(fmt.Sprintf("%s failed: %v", cmd, err)), true
	}
	return takeScreenshot()
}

// executeBashTool runs a bash command and returns its output.
func executeBashTool(input any) ([]anthropic.BetaToolResultBlockParamContentUnion, bool) {
	inputBytes, _ := json.Marshal(input)
	var params map[string]any
	json.Unmarshal(inputBytes, &params)

	command, _ := params["command"].(string)
	if command == "" {
		return errorResult("missing command"), true
	}

	fmt.Printf("  $ %s\n", command)
	out, err := exec.Command("bash", "-c", command).CombinedOutput()
	output := string(out)
	if err != nil {
		return textResult(fmt.Sprintf("Exit error: %v\n%s", err, output)), true
	}
	if output == "" {
		output = "(no output)"
	}
	return textResult(output), false
}

// executeTextEditorTool handles str_replace_based_edit_tool commands.
func executeTextEditorTool(input any) ([]anthropic.BetaToolResultBlockParamContentUnion, bool) {
	inputBytes, _ := json.Marshal(input)
	var params map[string]any
	json.Unmarshal(inputBytes, &params)

	command, _ := params["command"].(string)
	path, _ := params["path"].(string)

	fmt.Printf("  edit: %s %s\n", command, path)

	switch command {
	case "view":
		data, err := os.ReadFile(path)
		if err != nil {
			return errorResult(fmt.Sprintf("read failed: %v", err)), true
		}
		return textResult(string(data)), false

	case "create":
		fileText, _ := params["file_text"].(string)
		if err := os.WriteFile(path, []byte(fileText), 0644); err != nil {
			return errorResult(fmt.Sprintf("write failed: %v", err)), true
		}
		return textResult("File created."), false

	case "str_replace":
		oldStr, _ := params["old_str"].(string)
		newStr, _ := params["new_str"].(string)
		data, err := os.ReadFile(path)
		if err != nil {
			return errorResult(fmt.Sprintf("read failed: %v", err)), true
		}
		content := string(data)
		if !strings.Contains(content, oldStr) {
			return errorResult("old_str not found in file"), true
		}
		newContent := strings.Replace(content, oldStr, newStr, 1)
		if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
			return errorResult(fmt.Sprintf("write failed: %v", err)), true
		}
		return textResult("Replacement done."), false

	case "insert":
		insertLine, _ := params["insert_line"].(float64)
		newStr, _ := params["new_str"].(string)
		data, err := os.ReadFile(path)
		if err != nil {
			return errorResult(fmt.Sprintf("read failed: %v", err)), true
		}
		lines := strings.Split(string(data), "\n")
		idx := int(insertLine)
		if idx < 0 || idx > len(lines) {
			return errorResult("insert_line out of range"), true
		}
		lines = append(lines[:idx], append([]string{newStr}, lines[idx:]...)...)
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644); err != nil {
			return errorResult(fmt.Sprintf("write failed: %v", err)), true
		}
		return textResult("Insertion done."), false

	default:
		return errorResult(fmt.Sprintf("unknown command: %s", command)), true
	}
}

// takeScreenshot captures a screenshot and returns it as a base64-encoded image block.
func takeScreenshot() ([]anthropic.BetaToolResultBlockParamContentUnion, bool) {
	tmpFile := "/tmp/screenshot.png"
	if err := runCmd("scrot", "-o", tmpFile); err != nil {
		// Try import as fallback
		if err2 := runCmd("import", "-window", "root", tmpFile); err2 != nil {
			return errorResult(fmt.Sprintf("screenshot failed: scrot: %v, import: %v", err, err2)), true
		}
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		return errorResult(fmt.Sprintf("read screenshot failed: %v", err)), true
	}

	b64 := base64.StdEncoding.EncodeToString(data)
	return []anthropic.BetaToolResultBlockParamContentUnion{
		{OfImage: &anthropic.BetaImageBlockParam{
			Source: anthropic.BetaImageBlockParamSourceUnion{
				OfBase64: &anthropic.BetaBase64ImageSourceParam{
					MediaType: anthropic.BetaBase64ImageSourceMediaTypeImagePNG,
					Data:      b64,
				},
			},
		}},
	}, false
}

// helpers

func textResult(text string) []anthropic.BetaToolResultBlockParamContentUnion {
	return []anthropic.BetaToolResultBlockParamContentUnion{
		{OfText: &anthropic.BetaTextBlockParam{Text: text}},
	}
}

func errorResult(msg string) []anthropic.BetaToolResultBlockParamContentUnion {
	return textResult(msg)
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w (output: %s)", name, err, string(out))
	}
	return nil
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
