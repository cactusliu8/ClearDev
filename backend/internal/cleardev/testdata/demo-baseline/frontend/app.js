const form = document.querySelector("#import-form");
const accepted = document.querySelector('[data-testid="accepted-count"]');
const rejected = document.querySelector('[data-testid="rejected-count"]');
const duplicates = document.querySelector('[data-testid="duplicate-count"]');
const contacts = document.querySelector("#contacts");
const status = document.querySelector("#status");
const submit = document.querySelector("#import-submit");

async function refreshContacts() {
  if (!contacts) return;
  const response = await fetch("/api/contacts");
  if (!response.ok) throw new Error("Could not load contacts.");
  const result = await response.json();
  const items = result.contacts.map((contact) => {
    const item = document.createElement("li");
    item.textContent = contact.email;
    return item;
  });
  contacts.replaceChildren(...items);
}

function showError(error) {
  if (status) status.textContent = error instanceof Error ? error.message : "Request failed.";
}

form?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const textarea = document.querySelector("#emails");
  const text = textarea && "value" in textarea ? String(textarea.value) : "";
  if (submit) submit.disabled = true;
  if (status) status.textContent = "Importing…";
  try {
    const response = await fetch("/api/import", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ emails: text.split(/\r?\n/) }),
    });
    if (!response.ok) throw new Error(`Import failed (${response.status}).`);
    const result = await response.json();
    if (accepted) accepted.textContent = String(result.accepted);
    if (rejected) rejected.textContent = String(result.rejected);
    if (duplicates) duplicates.textContent = String(result.duplicates);
    await refreshContacts();
    if (status) status.textContent = "Import complete.";
  } catch (error) {
    showError(error);
  } finally {
    if (submit) submit.disabled = false;
  }
});

refreshContacts().catch(showError);
