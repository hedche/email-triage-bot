---
status: accepted
---

# Gemini runs on a billing-linked project, not the free tier

The Judge sends sender, subject and up to 2,000 characters of stripped body to Gemini. Google's Gemini API terms for the unpaid tier state that submitted content is used to train and improve its models, may be read by human reviewers, and should not include personal information. Linking a billing account (Tier 1) switches the project to the paid terms, under which prompts are not used for training, while the volume here costs pennies per month at Flash-Lite prices. The bot therefore documents billing-linked Gemini as the expected configuration and treats the free tier as unsupported for real mailboxes.

## Considered options

- **Free tier with headers and snippet only.** Reduces but does not remove the personal content shared, and weakens judgement quality.
- **Local inference only.** Fully private, but no node in the cluster has a GPU and this would make a hosted Judge impossible. Instead the bot ships an OpenAI-compatible Judge so a local model (Ollama, llama.cpp) or a hosted endpoint can be chosen per run; where that model runs is a separate infrastructure decision.

## Consequences

- Operators need a Google Cloud project with billing enabled and a budget alert, even though expected spend is near zero.
- The README must state plainly what leaves the network for each Judge implementation.
