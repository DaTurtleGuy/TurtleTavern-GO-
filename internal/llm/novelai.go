package llm

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/go-chi/chi/v5"
)

const (
	novelAPI  = "https://api.novelai.net"
	novelText = "https://text.novelai.net"
)

var novelBadWords = [][]int{
	{3}, {49356}, {1431}, {31715}, {34387}, {20765}, {30702}, {10691}, {49333}, {1266},
	{19438}, {43145}, {26523}, {41471}, {2936}, {85, 85}, {49332}, {7286}, {1115}, {24},
}

var novelEratoBadWords = [][]int{
	{16067}, {933, 11144}, {25106, 11144}, {58, 106901, 16073, 33710, 25, 109933},
	{933, 58, 11144}, {128030}, {58, 30591, 33503, 17663, 100204, 25, 11144},
}

var novelHypeBotBadWords = [][]int{
	{58}, {60}, {90}, {92}, {685}, {1391}, {1782}, {2361}, {3693}, {4083}, {4357}, {4895},
	{5512}, {5974}, {7131}, {8183}, {8351}, {8762}, {8964}, {8973}, {9063}, {11208},
	{11709}, {11907}, {11919}, {12878}, {12962}, {13018}, {13412}, {14631}, {14692},
	{14980}, {15090}, {15437}, {16151}, {16410}, {16589}, {17241}, {17414}, {17635},
	{17816}, {17912}, {18083}, {18161}, {18477}, {19629}, {19779}, {19953}, {20520},
	{20598}, {20662}, {20740}, {21476}, {21737}, {22133}, {22241}, {22345}, {22935},
	{23330}, {23785}, {23834}, {23884}, {25295}, {25597}, {25719}, {25787}, {25915},
	{26076}, {26358}, {26398}, {26894}, {26933}, {27007}, {27422}, {28013}, {29164},
	{29225}, {29342}, {29565}, {29795}, {30072}, {30109}, {30138}, {30866}, {31161},
	{31478}, {32092}, {32239}, {32509}, {33116}, {33250}, {33761}, {34171}, {34758},
	{34949}, {35944}, {36338}, {36463}, {36563}, {36786}, {36796}, {36937}, {37250},
	{37913}, {37981}, {38165}, {38362}, {38381}, {38430}, {38892}, {39850}, {39893},
	{41832}, {41888}, {42535}, {42669}, {42785}, {42924}, {43839}, {44438}, {44587},
	{44926}, {45144}, {45297}, {46110}, {46570}, {46581}, {46956}, {47175}, {47182},
	{47527}, {47715}, {48600}, {48683}, {48688}, {48874}, {48999}, {49074}, {49082},
	{49146}, {49946}, {10221}, {4841}, {1427}, {2602, 834}, {29343}, {37405}, {35780}, {2602}, {50256},
}

var novelRepPenaltyAllow = [][]int{
	{49256, 49264, 49231, 49230, 49287, 85, 49255, 49399, 49262, 336, 333, 432, 363, 468, 492, 745, 401, 426, 623, 794,
		1096, 2919, 2072, 7379, 1259, 2110, 620, 526, 487, 16562, 603, 805, 761, 2681, 942, 8917, 653, 3513, 506, 5301,
		562, 5010, 614, 10942, 539, 2976, 462, 5189, 567, 2032, 123, 124, 125, 126, 127, 128, 129, 130, 131, 132, 588,
		803, 1040, 49209, 4, 5, 6, 7, 8, 9, 10, 11, 12},
}

var novelEratoRepPen = []int{
	6, 1, 11, 13, 25, 198, 12, 9, 8, 279, 264, 459, 323, 477, 539, 912, 374, 574, 1051, 1550, 1587, 4536, 5828, 15058,
	3287, 3250, 1461, 1077, 813, 11074, 872, 1202, 1436, 7846, 1288, 13434, 1053, 8434, 617, 9167, 1047, 19117, 706,
	12775, 649, 4250, 527, 7784, 690, 2834, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 1210, 1359, 608, 220, 596, 956,
	3077, 44886, 4265, 3358, 2351, 2846, 311, 389, 315, 304, 520, 505, 430,
}

func novelBadWordsList(model string) [][]int {
	var list [][]int
	if strings.Contains(model, "hypebot") {
		list = novelHypeBotBadWords
	}
	if strings.Contains(model, "clio") || strings.Contains(model, "kayra") {
		list = novelBadWords
	}
	if strings.Contains(model, "erato") {
		list = novelEratoBadWords
	}
	out := make([][]int, len(list))
	copy(out, list)
	return out
}

func novelLogitBiasList(model string) []any {
	var out []any
	if strings.Contains(model, "erato") {
		out = append(out,
			map[string]any{"sequence": []int{12488}, "bias": -0.08, "ensure_sequence_finish": false, "generate_once": false},
			map[string]any{"sequence": []int{128041}, "bias": -0.08, "ensure_sequence_finish": false, "generate_once": false},
		)
	}
	if strings.Contains(model, "clio") || strings.Contains(model, "kayra") {
		out = append(out,
			map[string]any{"sequence": []int{23}, "bias": -0.08, "ensure_sequence_finish": false, "generate_once": false},
			map[string]any{"sequence": []int{21}, "bias": -0.08, "ensure_sequence_finish": false, "generate_once": false},
		)
	}
	return out
}

func novelRepPenWhitelist(model string) any {
	if strings.Contains(model, "clio") || strings.Contains(model, "kayra") {
		flat := make([]int, 0)
		for _, group := range novelRepPenaltyAllow {
			flat = append(flat, group...)
		}
		return flat
	}
	if strings.Contains(model, "erato") {
		out := make([]int, len(novelEratoRepPen))
		copy(out, novelEratoRepPen)
		return out
	}
	return nil
}

func isIntSlice(v any) bool {
	arr, ok := v.([]any)
	if !ok {
		return false
	}
	for _, item := range arr {
		if _, ok := item.(float64); !ok {
			return false
		}
	}
	return true
}

type NovelAIHandler struct {
	Cfg    *config.Config
	client *http.Client
}

func NewNovelAIHandler(cfg *config.Config) *NovelAIHandler {
	return &NovelAIHandler{Cfg: cfg, client: NewHTTPClient(cfg)}
}

func (h *NovelAIHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/novelai", func(r chi.Router) {
		r.Post("/status", h.Status)
		r.Post("/generate", h.Generate)
		r.Post("/generate-image", notImplemented("image generation is not implemented in this backend"))
		r.Post("/generate-voice", notImplemented("text-to-speech is not implemented in this backend"))
	})
}

func (h *NovelAIHandler) key(r *http.Request) string {
	return readSecret(userRootOf(r), "api_key_novel")
}

func (h *NovelAIHandler) Status(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if h.key(r) == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	res, err := DoJSON(h.client, r.Context(), http.MethodGet, novelAPI+"/user/subscription",
		map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + h.key(r)}, nil)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	if res.Status >= 200 && res.Status < 300 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(res.Body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"error":true}`))
}

func (h *NovelAIHandler) Generate(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if h.key(r) == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	model := bodyStr(body, "model")
	badWords := novelBadWordsList(model)
	if extra, ok := body["bad_words_ids"].([]any); ok {
		for _, bw := range extra {
			if arr, ok := bw.([]any); ok && isIntSlice(arr) {
				ints := make([]int, 0, len(arr))
				for _, n := range arr {
					ints = append(ints, int(n.(float64)))
				}
				badWords = append(badWords, ints)
			}
		}
	}
	var filteredBadWords [][]int
	for _, bw := range badWords {
		if len(bw) > 0 {
			filteredBadWords = append(filteredBadWords, bw)
		}
	}
	var badWordsOut any
	if len(filteredBadWords) > 0 {
		badWordsOut = filteredBadWords
	}
	logitBias := novelLogitBiasList(model)
	if extra, ok := body["logit_bias_exp"].([]any); ok {
		logitBias = append(logitBias, extra...)
	}
	repPenWhitelist := novelRepPenWhitelist(model)
	params := map[string]any{
		"use_string":                     withDefault(body["use_string"], true),
		"temperature":                    body["temperature"],
		"max_length":                     body["max_length"],
		"min_length":                     body["min_length"],
		"tail_free_sampling":             body["tail_free_sampling"],
		"repetition_penalty":             body["repetition_penalty"],
		"repetition_penalty_range":       body["repetition_penalty_range"],
		"repetition_penalty_slope":       body["repetition_penalty_slope"],
		"repetition_penalty_frequency":   body["repetition_penalty_frequency"],
		"repetition_penalty_presence":    body["repetition_penalty_presence"],
		"repetition_penalty_whitelist":   repPenWhitelist,
		"top_a":                          body["top_a"],
		"top_p":                          body["top_p"],
		"top_k":                          body["top_k"],
		"typical_p":                      body["typical_p"],
		"mirostat_lr":                    body["mirostat_lr"],
		"mirostat_tau":                   body["mirostat_tau"],
		"phrase_rep_pen":                 body["phrase_rep_pen"],
		"stop_sequences":                 body["stop_sequences"],
		"bad_words_ids":                  badWordsOut,
		"logit_bias_exp":                 logitBias,
		"generate_until_sentence":        body["generate_until_sentence"],
		"use_cache":                      body["use_cache"],
		"return_full_text":               body["return_full_text"],
		"prefix":                         body["prefix"],
		"order":                          body["order"],
		"num_logprobs":                   body["num_logprobs"],
		"min_p":                          body["min_p"],
		"math1_temp":                     body["math1_temp"],
		"math1_quad":                     body["math1_quad"],
		"math1_quad_entropy_scale":       body["math1_quad_entropy_scale"],
	}
	compactNils(params)
	data := map[string]any{"input": body["input"], "model": model, "parameters": params}
	if bodyStr(body, "prefix") == "theme_textadventure" {
		if strings.Contains(model, "clio") || strings.Contains(model, "kayra") {
			params["eos_token_id"] = 49405
		}
		if strings.Contains(model, "erato") {
			params["eos_token_id"] = 29
		}
	}
	headers := map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + h.key(r)}
	base := novelAPI
	if strings.Contains(model, "kayra") || strings.Contains(model, "erato") {
		base = novelText
	}
	streaming := bodyBool(body, "streaming")
	endpoint := base + "/ai/generate"
	if streaming {
		endpoint = base + "/ai/generate-stream"
	}
	if streaming {
		upstream, err := DoStream(h.client, r.Context(), http.MethodPost, endpoint, headers, data)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":true}`))
			return
		}
		ForwardStream(upstream, w, r)
		return
	}
	res, err := DoJSON(h.client, r.Context(), http.MethodPost, endpoint, headers, data)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	if res.Status < 200 || res.Status >= 300 {
		msg := string(res.Body)
		var parsed map[string]any
		if err := json.Unmarshal(res.Body, &parsed); err == nil {
			if m, ok := parsed["message"].(string); ok {
				msg = m
			}
		}
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res.Body)
}
