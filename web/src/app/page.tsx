import { redirect } from "next/navigation";

export default function Home() {
  // The one landing behaviour: signed-in users land on the board.
  redirect("/board");
}
