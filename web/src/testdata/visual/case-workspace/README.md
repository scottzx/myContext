# Approved wireframes — 经营事项工作台

These are the approved V1a design assets, copied into the repo on purpose
(design §9): CI must reference assets under version control, never a path in
someone's home directory. The originals were the design hand-off; from here on
this directory is the reference.

| File | What it fixes |
|---|---|
| `case-workspace-desktop.png` | The 1280+ layout: header, next node, timeline, right rail |
| `case-workspace-mobile.png` | The 390px layout: single column, next node above the title |
| `case-workspace-responsive.html` | The wireframe both were rendered from |

The three breakpoints that must stay honest are **390×844**, **768×1024** and
**1280+**. What the wireframes fix is not pixel styling — it is the information
order:

- 下一推进节点 comes before the title on a narrow screen.
- The timeline shows time, type, title, source entry point and confirm summary.
- Next actions come before the summary panels on a narrow screen.
- Nothing produces page-level horizontal scroll at 390px; wide content scrolls
  inside its own container.

Visual regression baselines are not generated yet — the harness for it is not in
this milestone. Until it is, these three files are the reference a change is
checked against by eye.
