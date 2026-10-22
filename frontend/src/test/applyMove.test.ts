import { applyMove } from "../components/Board";
import type { Column, Status, Task } from "../api/types";

const status = (key: string) => ({ key, name: key, category: "active", color: "#000", position: 0 }) as Status;
const t = (id: string, statusKey: string) => ({ id, statusKey }) as Task;
const cols = (): Column[] => [
  { status: status("a"), tasks: [t("1", "a"), t("2", "a")], count: 2 },
  { status: status("b"), tasks: [t("3", "b")], count: 1 },
];

describe("applyMove (optimistic board update)", () => {
  it("moves a card into another column before an anchor", () => {
    const out = applyMove(cols(), "1", "b", "3");
    expect(out[0].tasks.map((x) => x.id)).toEqual(["2"]);
    expect(out[1].tasks.map((x) => x.id)).toEqual(["1", "3"]);
    expect(out[1].tasks[0].statusKey).toBe("b");
    expect(out.map((c) => c.count)).toEqual([1, 2]);
  });
  it("appends when no anchor and reorders within a column", () => {
    expect(applyMove(cols(), "3", "a")[0].tasks.map((x) => x.id)).toEqual(["1", "2", "3"]);
    expect(applyMove(cols(), "2", "a", "1")[0].tasks.map((x) => x.id)).toEqual(["2", "1"]);
  });
  it("is a no-op for unknown tasks", () => {
    const c = cols();
    expect(applyMove(c, "zzz", "a")).toBe(c);
  });
});
