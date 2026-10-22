import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "../App";
import { createFakeApi } from "./fakeApi";

function columnNames(): string[] {
  const board = screen.getByRole("region", { name: "Kanban board" });
  return within(board).getAllByRole("heading", { level: 3 }).map((h) => h.textContent ?? "");
}

describe("Flowboard UI", () => {
  let fake: ReturnType<typeof createFakeApi>;

  beforeEach(() => {
    localStorage.clear();
    fake = createFakeApi();
    vi.stubGlobal("fetch", vi.fn(fake.fetch));
  });
  afterEach(() => vi.unstubAllGlobals());

  it("select list → sees that list's resolved columns → opens a task → edits a custom field → saves", async () => {
    const user = userEvent.setup();
    render(<App />);

    // Default selection is the first visible list (Backlog) with its own columns.
    await waitFor(() => expect(columnNames()).toEqual(["To Do", "In Progress", "In Review", "Done"]));

    // Sibling list resolves a different column set.
    await user.click(screen.getByRole("button", { name: /Sprint 12/ }));
    await waitFor(() => expect(columnNames()).toEqual(["Ready", "Blocked", "In Progress", "QA", "Done"]));
    expect(within(screen.getByTestId("column-qa")).getByText("Sprint item")).toBeInTheDocument();

    // Open the task drawer from the card.
    await user.click(screen.getByLabelText("Open Sprint item"));
    const dialog = await screen.findByRole("dialog", { name: "Task details" });
    const points = await within(dialog).findByLabelText(/Story Points/);
    expect(points).toHaveValue(3);

    // Edit the custom field and save.
    await user.clear(points);
    await user.type(points, "8");
    await user.click(within(dialog).getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(document.querySelector(".toast")).toHaveTextContent("Saved “Sprint item”"));
    const patch = fake.requests.find((r) => r.method === "PATCH" && r.path === "/tasks/t2");
    expect(patch?.body).toEqual({ version: 1, customFields: { story_points: 8 } });
    expect(patch?.user).toBe("priya");
    expect(fake.tasks.find((t) => t.id === "t2")?.customFields.story_points).toBe(8);
  });

  it("shows the server's field error next to the offending custom field", async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: /Sprint 12/ }));
    await user.click(await screen.findByLabelText("Open Sprint item"));
    const dialog = await screen.findByRole("dialog");
    await user.selectOptions(await within(dialog).findByLabelText(/Component/), "");
    await user.click(within(dialog).getByRole("button", { name: "Save changes" }));
    expect(await within(dialog).findByText("Component is required")).toBeInTheDocument();
    expect(within(dialog).getByText("Fix the highlighted fields and save again.")).toBeInTheDocument();
    expect(fake.tasks.find((t) => t.id === "t2")?.customFields.component).toBe("frontend");
  });

  it("switching the acting user from Priya to Rahul hides the private list", async () => {
    const user = userEvent.setup();
    render(<App />);
    const tree = await screen.findByRole("tree");
    await waitFor(() => expect(within(tree).getByText("Security Audit")).toBeInTheDocument());

    await user.selectOptions(screen.getByLabelText("Acting as"), "rahul");

    await waitFor(() => expect(within(screen.getByRole("tree")).queryByText("Security Audit")).not.toBeInTheDocument());
    expect(within(screen.getByRole("tree")).getByText("Sprint 12")).toBeInTheDocument();
    expect(fake.requests.at(-1)?.user).toBe("rahul");
  });
});
