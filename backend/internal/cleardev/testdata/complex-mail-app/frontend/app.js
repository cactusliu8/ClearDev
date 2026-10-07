const form = document.querySelector("#import-form");
const accepted = document.querySelector('[data-testid="accepted-count"]');
const rejected = document.querySelector('[data-testid="rejected-count"]');
const duplicates = document.querySelector('[data-testid="duplicate-count"]');

form?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const textarea = document.querySelector("#emails");
  const text = textarea && "value" in textarea ? String(textarea.value) : "";
  const emails = text.split(/\r?\n/);
  const response = await fetch("/api/import", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ emails }),
  });
  if (!response.ok) {
    throw new Error("import failed");
  }
  const result = await response.json();
  if (accepted) accepted.textContent = String(result.accepted ?? 0);
  if (rejected) rejected.textContent = String(result.rejected ?? 0);
  if (duplicates) duplicates.textContent = String(result.duplicates ?? 0);
});
