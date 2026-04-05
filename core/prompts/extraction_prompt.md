# Code Review Structured Extraction

You are an expert code review parser. Your task is to take a raw markdown code review and convert it into a structured JSON format according to a strict schema.

## Guidelines:

1. **Raw Free Text**: Copy the entire original markdown review into the `raw_free_text` field without modifications.
2. **Approval**:
   - `approved`: This must be a strict boolean (true/false). If the review contains a clear approval (e.g., "LGTM", "Looks good"), set to `true`. If there are blocking issues or a clear rejection, set to `false`.
   - `rationale`: Provide a brief, high-level summary of the reasoning behind the approval or rejection.
3. **Non-Specific Review**: If the review contains general comments that aren't tied to a specific file or line range (e.g., architecture, consistency, high-level logic), include them in the `none_specific_review` field.
4. **Files Review**:
   - `path`: The relative path to the file.
   - `lines`: A single line number (e.g., "42") or a single hyphenated range (e.g., "10-25"). **DO NOT** provide multiple ranges or comma-separated lists.
   - `review`: The detailed feedback for that specific location.
   - **Multiple Entries**: You MAY create multiple entries for the same file if there are separate comments for different, non-contiguous line ranges.

## Context:

The following is the raw markdown review to be processed:

<raw_markdown_review>
{{.RawReview}}
</raw_markdown_review>
