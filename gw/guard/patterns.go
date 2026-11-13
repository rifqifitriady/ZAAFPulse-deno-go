package guard

import "github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"

// The pattern sources and flags below are copied verbatim (re.source, re.flags) from lib/signals.js and
// lib/evals/pii.js, printed by Node; the fixtures in gw/testdata/guard check that they behave the same.

// jsSources are the SHA-256 hashes of the JS source this port follows line by line (gen-guard.js writes the current
// ones into gw/testdata/guard/patterns-all.json). When a hash changes, re-read that JS, port the change, and update
// the hash here: patterns_test.go fails until then.
var jsSources = map[string]string{
	"lib/signals.js#injection-and-secrets": "b2cf6e0fbae128588c5129f8130e87f7bba0d0755750136aadd696f2bb2e8f65",
	"lib/evals/pii.js":                     "4ba62a07014bf3c89a3742f62d91722f1eace03231807ae9b5a1aaaaaf4dfb7d",
	"lib/gateway/guardrails.js":            "e2f7c481ea8627c971148188456c284482011bcf08369745a273236b1ada61b5",
}

type pattern struct{ src, flags string }

func compile(p pattern) *jsre.Regexp { return jsre.MustCompile(p.src, p.flags) }

func compileAll(ps []pattern) []*jsre.Regexp {
	out := make([]*jsre.Regexp, len(ps))
	for i, p := range ps {
		out[i] = compile(p)
	}
	return out
}

type injectionCategory struct {
	id, label           string
	carries, supporting bool
	res                 []*jsre.Regexp
}

type secretPattern struct {
	label string
	re    *jsre.Regexp
}

var injection = []injectionCategory{
	{id: "exfil", label: "Exfiltration request", carries: false, supporting: false, res: compileAll([]pattern{
		{"(?<!(?:not|n't|never|jangan) |\\.)\\b(?:send|post|upload|exfiltrate|forward|e-?mail|transmit|leak|paste|curl|wget|push|sync|commit|publish)\\b[^.\\n]{0,50}?(?:api[ _-]?keys?|credentials?|secrets?|tokens?|passwords?|env(?:ironment)? (?:variables|vars)|\\.env\\b|ssh keys?|id_rsa|id_ed25519|private keys?|cookies|~\\/\\.ssh|~\\/\\.aws|keychain)[^.\\n]{0,60}?(?:\\bto\\b|\\binto\\b|\\bvia\\b|https?:\\/\\/|@[\\w-]+\\.)", "i"},
		{"(?<!(?:not|n't|never) |\\.)\\b(?:print|reveal|output|dump|display|show|append|include|leak|tell me|attach)\\b[^.\\n]{0,30}?(?:(?:hidden|system|secret|original|internal) (?:instructions|prompt)|system prompt|api[ _-]?keys?|credentials|secrets|env(?:ironment)? variables|\\.env file|ssh keys?|private keys?|~\\/\\.aws\\/credentials|~\\/\\.ssh\\/id_\\w+|aws credentials|\\.env\\b)", "i"},
		{"(?:~\\/\\.aws\\/credentials|~\\/\\.ssh\\/id_\\w+|\\.env\\b|id_rsa|\\/etc\\/passwd)[^\\n]{0,80}?\\|\\s*(?:base64\\s*\\|\\s*)?(?:curl|wget|nc|ncat)\\b", "i"},
		{"\\b(?:cat|type|less|base64|print|open|read)\\s+(?:~\\/\\.ssh\\/id_\\w+|~\\/\\.aws\\/credentials|~?\\/?\\.env\\b|id_rsa)[^\\n]{0,120}?\\b(?:curl|wget|nc|fetch|url|webhook|query string|request|send|post|upload|reply|response|answer|code block)\\b", "i"},
		{"(?<!jangan )\\b(?:kirim(?:kan|in)?|unggah|upload|bocorkan|teruskan|salin|tempel(?:kan)?|post)\\b[^.\\n]{0,40}?(?:token|kunci api|api key|kredensial|kata sandi|password|rahasia|secret|\\.env|kunci ssh|ssh key|kunci privat|cookie)[^.\\n]{0,60}?(?:\\bke\\b|https?:\\/\\/|\\bvia\\b|\\bmelalui\\b)", "i"},
		{"\\b(?:tampilkan|tunjukkan|bocorkan|cetak|tuliskan)\\b[^.\\n]{0,30}?(?:system prompt|prompt sistem|kredensial|kunci api|api key|rahasia|isi file \\.env)", "i"},
	})},
	{id: "command", label: "Command injection", carries: false, supporting: false, res: compileAll([]pattern{
		{"\\b(?:run|execute|exec|type|paste|enter)\\b (?:the following|this|these|that) (?:commands?|scripts?|code)\\b[^.\\n]{0,40}?\\b(?:immediately|now|right away|without (?:asking|confirmation|confirming|prompting|approval|checking))\\b", "i"},
		{"\\bthe (?:assistant|agent|model|ai) (?:must|should|is required to|has to) (?:also )?(?:call|use|invoke|run|execute|send)\\b|\\b(?:run|execute)\\b[^.\\n]{0,80}?\\b(?:and )?(?:do not|don'?t|never) ask\\b", "i"},
		{"\\b(?:run|execute)\\b[^.\\n]{0,60}?\\bwithout (?:asking|confirmation|confirming|prompting|approval|permission|checking with)\\b", "i"},
		{"\\b(?:jalankan|eksekusi)\\b[^.\\n]{0,60}?\\b(?:tanpa (?:bertanya|konfirmasi|izin|persetujuan)|sekarang juga)\\b", "i"},
	})},
	{id: "override", label: "Instruction override", carries: false, supporting: false, res: compileAll([]pattern{
		{"\\b(?:ignore|disregard|forget|override|bypass|discard|abandon|neglect)\\b(?:\\s+(?:all|any|every|each|the|your|my|of|these|those|everything|previous|prior|above|earlier|preceding|foregoing|original|initial|system|existing|old|current|other|developer'?s?|user'?s?))*\\s+(?:instructions?|prompts?|guidance|guidelines|directions|directives|rules|context|constraints|programming|orders|policies|policy|safety (?:checks|rules|guidelines)|restrictions|guardrails|messages)\\b", "i"},
		{"\\b(?:ignore|disregard|forget|override)\\s+(?:all\\s+|any\\s+)?(?:the\\s+)?(?:user'?s?|human'?s?|previous|prior|original|earlier|your|their|operator'?s?)\\s+(?:requests?|tasks?|commands?|wishes|questions?)\\b|\\bstop what you(?:'re| are) doing\\b|\\b(?:ignore|disregard|forget)\\s+(?:the|your|this)\\s+(?:task|request|instructions?|prompt)s?\\s+(?:above|before|so far)\\b|\\b(?:skip|bypass|ignore|disable) (?:the |all |any )?(?:permission|confirmation|approval|safety) (?:prompts?|checks?|requests?|dialogs?)\\b|\\bfollow only the instructions (?:on|in) this (?:page|document|file)\\b", "i"},
		{"\\blupakan (?:semua|segala|apa)\\b[^.\\n]{0,30}?\\b(?:sebelumnya|dikatakan|diberitahu|diperintahkan)\\b|\\b(?:turuti|menuruti|ikuti|mengikuti) (?:hanya )?perintah (?:di|dari) (?:halaman|dokumen|situs|file) ini\\b", "i"},
		{"\\bforget (?:everything|all|what)\\b[^.\\n]{0,25}?\\b(?:told|above|before|so far|said)\\b", "i"},
		{"\\b(?:abaikan(?:lah)?|lupakan|acuhkan|hiraukan|langgar|jangan (?:ikuti|hiraukan|pedulikan))\\b(?:\\s+(?:semua|seluruh|segala|setiap|tiap|itu|ini))*\\s+(?:instruksi|perintah|aturan|arahan|petunjuk|pedoman|prompt|konteks|batasan|kebijakan)\\b", "i"},
		{"\\bignora(?:r)? (?:todas? )?(?:las |los )?(?:instrucciones|reglas|indicaciones)\\b", "i"},
		{"\\bignore[zr]? (?:toutes? )?(?:les )?(?:instructions|règles|consignes) (?:précédentes|antérieures)", "i"},
		{"\\bignorier(?:e|en)? (?:alle )?(?:vorherigen |bisherigen )?(?:anweisungen|regeln|instruktionen)", "i"},
		{"\\bignore (?:todas )?as instruções", "i"},
		{"忽略(?:之前|以上|先前|上面)?(?:的)?(?:所有)?(?:指令|指示|说明|规则)", ""},
	})},
	{id: "hidden", label: "Hide-from-user instruction", carries: false, supporting: false, res: compileAll([]pattern{
		{"\\b(?:don'?t|do not|never) (?:tell|inform|mention|reveal|show|notify|alert|let) (?:this |it |that )?(?:to )?(?:the )?(?:user|human|operator)\\b|\\bwithout (?:telling|informing|alerting|notifying|asking) the (?:user|human)\\b|\\bkeep (?:this|it) (?:secret|hidden) from (?:the )?(?:user|human)", "i"},
		{"\\b(?:don'?t|do not|never) (?:mention|include|report|note|disclose) (?:this|it|that)\\b[^.\\n]{0,20}?\\b(?:summary|report|reply|response|answer|output)\\b|\\b(?:do (?:this|it)|proceed) (?:silently|quietly|secretly)\\b|\\b(?:then )?(?:say|claim|report) (?:that )?(?:the task|it|everything) (?:is|was) (?:complete|done|finished|successful)\\b|\\b(?:should|must|will) not be (?:shown|mentioned|revealed|disclosed|reported) to the (?:user|human)\\b|\\bwhen you summari[sz]e\\b[^.\\n]{0,40}?\\b(?:omit|hide|leave out|skip)\\b|\\b(?:omit|hide|leave out) (?:this|it|that)(?: line| part| section| step)?\\b[^.\\n]{0,30}?\\b(?:summar\\w*|report|reply|response|the user)\\b|\\btell the user (?:that )?(?:it|this|the (?:page|site|file|script)) is safe\\b", "i"},
		{"(?:^|[.!?:]\\s+|\\n)\\s*(?:quietly|silently|secretly|covertly)\\b[^.\\n]{0,20}?\\b(?:add|run|execute|install|modify|send|upload|change|delete|append|insert)\\b", "i"},
		{"\\bjangan (?:beri ?tahu|kasih ?tahu|bilang|beritahukan|laporkan|tunjukkan)(?: (?:hal )?(?:ini|itu))?(?: (?:ke|kepada))? (?:pengguna|user|pemakai)\\b|\\btanpa (?:memberi ?tahu|sepengetahuan|bilang ke) (?:pengguna|user)\\b|\\bjangan (?:sebutkan|sebut|ceritakan)\\b[^.\\n;]{0,30}?\\b(?:tersembunyi|ini|itu|skrip|perintah)\\b|\\b(?:balas|bilang|katakan) (?:ke |kepada )?pengguna bahwa\\b|\\b(?:secara )?diam-diam\\b[^.\\n]{0,20}?\\b(?:tambahkan|jalankan|kirim\\w*|unggah|ubah|hapus|install|pasang|masukkan)\\b", "i"},
	})},
	{id: "persona", label: "Persona hijack", carries: false, supporting: false, res: compileAll([]pattern{
		{"\\byou are no longer (?:a|an|the|just)\\b|\\byou are now (?:DAN|in (?:developer|god|admin|maintenance|debug|jailbreak|unrestricted) mode|an? (?:unrestricted|unfiltered|jailbroken|uncensored|evil)\\b)|\\b(?:enable|enter|activate|switch to) (?:developer|god|admin|jailbreak|DAN|unrestricted) mode\\b|\\bact as (?:DAN|an? (?:unrestricted|unfiltered|uncensored|jailbroken))|\\b(?:from now on|henceforth),? you (?:are|will be|act as|must act as)\\b|\\bpretend (?:to be|you are) (?:an? )?(?:unrestricted|unfiltered|DAN|evil|jailbroken)|\\b(?:AI|assistant|model) (?:without|with no) (?:restrictions|limits|filters|rules|guidelines)\\b", "i"},
		{"\\b(?:mulai )?sekarang (?:kamu|anda) (?:adalah|menjadi|berperan sebagai)\\b|\\bmulai sekarang (?:kamu|anda) (?:harus|wajib)\\b|\\b(?:kamu|anda) (?:sekarang )?(?:adalah|menjadi) (?:asisten|AI|model) (?:tanpa|yang tidak punya) (?:batasan|aturan|filter)", "i"},
	})},
	{id: "authority", label: "Fake authority", carries: false, supporting: false, res: compileAll([]pattern{
		{"\\bnew (?:instructions?|task|request) from (?:the )?(?:user|developer|admin|administrator|system|operator|openai|anthropic|owner)\\b|\\b(?:important|attention|urgent|system|admin)\\s*[:!-]\\s*(?:the )?(?:new|updated) (?:instructions?|rules?|directives?)\\b|\\b(?:your|the) (?:new|real|actual|true) (?:instructions|directive|objective) (?:is|are)\\b|\\bI (?:hereby )?authori[sz]e you to\\b|\\b(?:has|have|had|is|was|are|were|been|already) pre-?approved\\b|\\bpre-?approved (?:this|it|all|every|the following)\\b|\\b(?:the )?(?:admin|administrator|developer|maintainers?|owner|operator) (?:said|says|has said|confirmed) (?:that )?you (?:may|can|are allowed|should|must)\\b|\\b(?:the )?user (?:has )?(?:already )?(?:pre-?)?approved (?:all|every|this|it)\\b", "i"},
		{"\\binstruksi baru dari (?:developer|admin|sistem|pengembang)\\b|\\bpengguna sudah (?:menyetujui|mengizinkan) semua\\b", "i"},
	})},
	{id: "roletag", label: "Fake role tags", carries: true, supporting: false, res: compileAll([]pattern{
		{"<\\/?(?:system|im_start|im_end)>|\\[\\/?INST\\]|<\\|im_start\\|>|<\\|im_end\\|>|<\\|(?:system|user|assistant)\\|>|\\[(?:SYSTEM|ADMIN|DEVELOPER)(?: (?:MESSAGE|PROMPT|OVERRIDE|NOTE))?\\]|BEGIN SYSTEM PROMPT|<<SYS>>|### (?:System|Instruction):|(?:^|\\n)\\s*(?:system|assistant|developer)\\s*:(?!\\s*['\"`{\\[])", "i"},
	})},
	{id: "pipeshell", label: "Download piped to a shell", carries: false, supporting: true, res: compileAll([]pattern{
		{"\\b(?:curl|wget)\\b[^\\n|`']{0,150}\\|\\s*(?:sudo\\s+)?(?:ba|z)?sh\\b", "i"},
	})},
	{id: "addressed", label: "Addressed to the AI", carries: false, supporting: true, res: compileAll([]pattern{
		{"\\b(?:note|message|attention|instructions?|notice|important) (?:to|for) (?:any |all )?(?:the )?(?:ai|llm|language model|assistant|agent|chatbot|claude|chatgpt|gpt|copilot|codex|gemini)s?\\b|\\b(?:attention|hey|dear|hello),? (?:ai|llm|claude|chatgpt|gpt|copilot|codex|gemini)(?: agents?| assistants?| models?)?\\s*[:!,]|\\b(?:ai|llm) (?:coding )?(?:agents?|assistants?|models?)\\b[^.\\n]{0,10}?(?:reading|processing|parsing|working in|:)|\\bif you are an? (?:ai|llm|language model|assistant|agent|bot)\\b|(?:^|\\n)\\s*(?:claude|chatgpt|codex|copilot|gemini)\\s*,", "i"},
		{"\\bcatatan (?:untuk|bagi) (?:ai|asisten|agen|llm)\\b|\\b(?:halo|hai|hei),? (?:ai|claude|asisten)\\b|\\b(?:jika|kalau) (?:kamu|anda) (?:membaca ini|adalah ai)\\b|\\b(?:untuk|bagi|kepada) (?:para )?(?:ai|agen ai|asisten ai|llm)\\s*[:,]", "i"},
	})},
}

// INJ_CARRIES
var reInjCarries = compile(pattern{"\\b(?:you (?:are|must|will|should|have to|may)|ignore|disregard|override|from now on|reveal|bypass|disable|comply|do not|don'?t|always|never|mode|execute|kamu|abaikan|pre-?approved|instead)\\b", "i"})

// INJ_ANCHOR
var reInjAnchor = compile(pattern{"\\b(?:ignor|disregard|forget|overrid|bypass|discard|abandon|neglect|abaikan|lupakan|hiraukan|acuhkan|langgar|jangan|you are (?:now|no longer)|(?:developer|god|admin|jailbreak|dan|unrestricted|maintenance|debug) mode|act as|from now on|henceforth|pretend|without|exfil|credential|secret|token|passw|api[ _-]?key|ssh key|id_rsa|id_ed25519|private key|env(?:ironment)? (?:variables|vars)|system prompt|prompt sistem|(?:hidden|original|internal) prompt|kredensial|kunci|rahasia|kata sandi|quietly|silently|secretly|covertly|diam-diam|im_start|im_end|immediately|right away|tanpa|sekarang|instruction|instruksi|perintah (?:di|dari)|directive|objective|(?:new|updated) rules?|(?:task|request) from|said you|says you|confirmed (?:that )?you|authori[sz]e|approved|pre-approved|menyetujui|mengizinkan|attention|note (?:to|for)|if you are an?|catatan|halo|hai|hei|(?:kalau|jika) (?:kamu|anda)|ai|llm|claude|chatgpt|codex|copilot|gemini|cookie|keychain|(?:following|this|these|that) (?:commands?|scripts?|code)|(?:assistant|agent|model) (?:must|should|is required|has to)|(?:not|never) ask|n't ask|stop what you|(?:permission|confirmation|approval|safety) (?:prompts?|checks?|requests?|dialogs?)|(?:mention|tell|inform|notify|alert|reveal|show|let) (?:this |it |that )?(?:to )?(?:the )?(?:user|human|operator)|hidden from|the task (?:is|was) (?:complete|done|finished|successful)|not be (?:shown|mentioned|revealed|disclosed|reported)|when you summari|(?:omit|hide|leave out) (?:this|it|that)|pengguna bahwa|curl|wget|(?:system|assistant|developer)[ \\t]*:)", "gi"})

// INJ_ANCHOR_MARK
var reInjAnchorMark = compile(pattern{"<\\/?system>|\\[\\/?inst\\]|<\\||<<sys>>|### (?:system|instruction)|\\.env|\\.ssh|\\.aws|\\[(?:system|admin|developer)|忽略", "gi"})

// DOC_CONTEXT
var reDocContext = compile(pattern{"\\b(?:attack(?:er|ers|s)?|for example|e\\.g\\.|for instance|such as|prompt injections?|injection(?: attacks?)?|jailbreaks?|red[- ]team(?:ing)?|detect(?:ion|or|s|ed)?|regex|owasp|mitigat\\w*|defen[cs]es?|defend|classifier|malicious|adversarial|well-known|known (?:prompt|attack)s?|contoh|serangan|penyerang|misalnya|deteksi)\\b", "i"})

// CODE_LINE
var reCodeLine = compile(pattern{"\\bre:\\s*\\/|new RegExp|\\.test\\(|\\.match\\(|\\bexpect\\(|\\bassert\\w*\\(|\\b(?:it|test|describe)\\(\\s*['\"`]|=>|^\\s*(?:\\d+[\\t→]\\s*)?(?:const|let|var|def|return)\\b|=\\s*\\/[^/\\n]+\\/[gimsuy]*|^\\s*\\/.+\\/[gimsuy]*[,;]?\\s*$|^\\s*(?:\\d+[\\t→]\\s*)?[A-Za-z_$][\\w$]*:\\s*['\"`]", ""})

// STRING_ITEM
var reStringItem = compile(pattern{"^\\s*(?:\\d+[\\t→]\\s*)?(['\"`]).*\\1,?\\s*$", ""})

// QUOTE_BEFORE
var reQuoteBefore = compile(pattern{"[\"'“‘`«]\\s*$", ""})

// ZERO_WIDTH
var reZeroWidth = compile(pattern{"[\\u00ad\\u200b-\\u200f\\u2060\\ufeff]", "g"})

// SPLIT_LETTERS
var reSplitLetters = compile(pattern{"\\b(?:[a-z][-_.*·•\\s]){3,}[a-z]\\b", "gi"})

// HAS_ZERO_WIDTH
var reHasZeroWidth = compile(pattern{"[\\u00ad\\u200b-\\u200f\\u2060\\ufeff]", ""})

// HAS_SPLIT
var reHasSplit = compile(pattern{"\\b(?:[a-z][-_.*·•\\s]){3,}[a-z]\\b", "i"})

// HAS_LEET
var reHasLeet = compile(pattern{"\\b[a-z]+[0134][a-z]+[0134]?[a-z]*\\b", "i"})

// SECRET_HINT
var reSecretHint = compile(pattern{"sk-|AKIA|ASIA|gh[pousr]_|github_pat_|xox|AIza|k_live_|k_test_|hf_|eyJ|PRIVATE KEY|:\\/\\/", ""})

// PLACEHOLDER
var rePlaceholder = compile(pattern{"your|example|placeholder|dummy|sample|xxxx|replace|redacted|\\.\\.\\.|[<>]|\\*\\*|••", "i"})

// PLACEHOLDER_PASSWORD
var rePlaceholderPassword = compile(pattern{"^(?:password|passwd|pass|pwd|secret|changeme|example|test|root|admin|user|x{3,}|\\*+|•+|<[^>]*>|\\$\\{?[A-Za-z_]\\w*\\}?|\\{\\{.*\\}\\}|%[A-Z_]+%|\\[.*\\])$", "i"})

var secretPatterns = []secretPattern{
	{"Anthropic API key", compile(pattern{"\\bsk-ant-[A-Za-z0-9_-]{20,}", "g"})},
	{"OpenRouter API key", compile(pattern{"\\bsk-or-v1-[A-Fa-f0-9]{32,}", "g"})},
	{"OpenAI API key", compile(pattern{"\\bsk-(?!ant-|or-)(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{32,}", "g"})},
	{"AWS access key", compile(pattern{"\\b(?:AKIA|ASIA)[0-9A-Z]{16}\\b", "g"})},
	{"GitHub token", compile(pattern{"\\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,})", "g"})},
	{"Slack token", compile(pattern{"\\bxox[abposr]-[A-Za-z0-9-]{10,}", "g"})},
	{"Google API key", compile(pattern{"\\bAIza[0-9A-Za-z_-]{35}\\b", "g"})},
	{"Stripe secret key", compile(pattern{"\\b[sr]k_(?:live|test)_[A-Za-z0-9]{20,}", "g"})},
	{"Hugging Face token", compile(pattern{"\\bhf_[A-Za-z0-9]{34,}\\b", "g"})},
	{"JSON Web Token", compile(pattern{"\\beyJ[A-Za-z0-9_-]{8,}\\.eyJ[A-Za-z0-9_-]{8,}\\.[A-Za-z0-9_-]{16,}", "g"})},
	{"Private key", compile(pattern{"-----BEGIN [A-Z ]*PRIVATE KEY-----", "g"})},
	{"Database URL with password", compile(pattern{"\\b(?:postgres(?:ql)?|mysql|mariadb|mongodb(?:\\+srv)?|rediss?|amqps?|mssql|sqlserver|cockroachdb)(?:\\+\\w+)?:\\/\\/[^\\s:/@'\"`]*:([^\\s@'\"`/]{3,})@[\\w.-]+", "gi"})},
}

var reEmail = compile(pattern{"(?<![a-z0-9._%+-])[a-z0-9._%+-]+@[a-z0-9-]+(?:\\.[a-z0-9-]+)*\\.(?!(?:png|jpe?g|svg|webp|gif|ico|bmp|avif|tiff?)\\b)[a-z]{2,}\\b(?!\\.[a-z0-9])", "gi"})
var reIPV4 = compile(pattern{"(?<![\\w.@]|(?<!\\/)\\/)(?:25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)(?:\\.(?:25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)){3}(?!\\w|\\.\\d)", "g"})
var reIPV6 = compile(pattern{"(?<![\\w:.])(?:(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}|(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*)?::(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*)?)(?![\\w:])", "gi"})
var rePhone = compile(pattern{"(?<![\\w.:/+-])(?:\\+\\d[\\d ().-]{7,20}\\d|\\(\\d{2,4}\\)[ -]?\\d{2,4}(?:[ -]\\d{2,4}){1,3}|\\d{2,4}(?:-\\d{2,4}){2,4})(?![\\w:/-]|\\.\\d)", "g"})
